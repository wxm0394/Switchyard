package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wxm0394/Switchyard/internal/crypto"
	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
)

// Phase 8 Plan/Act battery. All routes/keys/aliases are fake; the
// upstream is a local httptest server (except the Groq route, whose
// BaseURL is api.groq.com on purpose: large-request rotation must
// exclude it from first-pick, so it is never actually called in the
// large tests below). No real keys, no real upstream, no Ohio/RN.
type planActSetup struct {
	gw       *Gateway
	mem      *store.MemStore
	captured *[]string
	mu       *sync.Mutex
}

func newPlanActGateway(t *testing.T) *planActSetup {
	t.Helper()
	captured := []string{}
	mu := &sync.Mutex{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		captured = append(captured, req["model"].(string))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","choices":[{"message":{"content":"4"},"finish_reason":"stop"}],"usage":{"total_tokens":195}}`))
	}))
	t.Cleanup(up.Close)
	mem := store.NewMemStore()
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 7)
	}
	addKey := func(id string) {
		enc, err := crypto.Encrypt(keyBytes, "fake-key-"+id)
		if err != nil {
			t.Fatal(err)
		}
		_ = mem.UpsertKey(scheduler.ProviderKey{ID: id, Name: id, ProviderType: "openai-compatible", EncryptedKey: enc, KeyMask: "fake••••key", KeyState: "healthy", BaseURL: up.URL + "/v1"})
	}
	addRoute := func(id, model, key, base string, vision, tools bool, maxCtx int) {
		_ = mem.UpsertRoute(scheduler.Route{ID: id, Name: model, ProviderKeyID: key, ModelID: model, DisplayName: model, BaseURL: base, ProviderType: "openai-compatible", UpstreamModel: model, TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsVision: vision, SupportsTools: tools, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: maxCtx, ReliabilityScore: 100, CostWeight: 1, Enabled: true})
	}
	// Keys: one per route, so quota/rotation accounting is per route.
	for _, k := range []string{"k-kaggle", "k-qwen", "k-nvidia", "k-groq", "k-laguna", "k-glm", "k-fastonly", "k-visiononly"} {
		addKey(k)
	}
	// Production-shaped modelIds. Groq MaxContext is deliberately
	// high (200000) to isolate rotation exclusion from the
	// HardFilter context filter: in production Groq is 32768, so a
	// very large request would ALSO be context-filtered; rotation
	// exclusion is an additional first-pick guard, proved here
	// independently of context.
	addRoute("r-kaggle", "google/gemini-3-flash-preview", "k-kaggle", up.URL+"/v1", true, true, 200000)
	addRoute("r-qwen", "qwen3.6-27b", "k-qwen", up.URL+"/v1", false, true, 200000)
	addRoute("r-nvidia", "nvidia/nemotron-3.5-lightning-30b-a3b", "k-nvidia", up.URL+"/v1", false, true, 200000)
	addRoute("r-groq", "openai/gpt-oss-20b", "k-groq", "https://api.groq.com/openai/v1", false, true, 200000)
	addRoute("r-laguna", "poolside/laguna-xs-2.1:free", "k-laguna", up.URL+"/v1", true, false, 200000)
	addRoute("r-glm", "glm-4.5", "k-glm", up.URL+"/v1", false, true, 200000)
	addRoute("r-fastonly", "fast-only-model", "k-fastonly", up.URL+"/v1", false, true, 200000)
	addRoute("r-visiononly", "vision-only-model", "k-visiononly", up.URL+"/v1", true, false, 200000)
	// Alias orders mirror production (coder: NVIDIA before Groq;
	// fast: Groq before NVIDIA; reasoning: Kaggle, Groq, NVIDIA).
	_ = mem.UpsertAlias(store.Alias{ID: "a-coder", Alias: "coder", TargetModels: []string{"google/gemini-3-flash-preview", "qwen3.6-27b", "nvidia/nemotron-3.5-lightning-30b-a3b", "openai/gpt-oss-20b", "poolside/laguna-xs-2.1:free", "glm-4.5"}, Description: "coder"})
	_ = mem.UpsertAlias(store.Alias{ID: "a-fast", Alias: "fast", TargetModels: []string{"google/gemini-3-flash-preview", "qwen3.6-27b", "openai/gpt-oss-20b", "nvidia/nemotron-3.5-lightning-30b-a3b", "poolside/laguna-xs-2.1:free", "glm-4.5", "fast-only-model"}, Description: "fast"})
	_ = mem.UpsertAlias(store.Alias{ID: "a-reasoning", Alias: "reasoning", TargetModels: []string{"google/gemini-3-flash-preview", "openai/gpt-oss-20b", "nvidia/nemotron-3.5-lightning-30b-a3b", "poolside/laguna-xs-2.1:free", "glm-4.5"}, Description: "reasoning"})
	_ = mem.UpsertAlias(store.Alias{ID: "a-vision", Alias: "vision", TargetModels: []string{"google/gemini-3-flash-preview", "poolside/laguna-xs-2.1:free"}, Description: "vision"})
	_ = mem.UpsertToken(store.ClientToken{ID: "t1", Name: "test", Token: "sk-portal-test", CreatedAt: "2026-01-01T00:00:00Z", Active: true})
	gw := New(mem, Config{MasterKey: keyBytes, Decrypt: func(enc string) (string, error) { return crypto.Decrypt(keyBytes, enc) }})
	return &planActSetup{gw: gw, mem: mem, captured: &captured, mu: mu}
}

func (s *planActSetup) doChat(t *testing.T, model, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-portal-test")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	_ = model // model is inside body; kept for call-site clarity
	rec := httptest.NewRecorder()
	s.gw.HandleChat(rec, req)
	return rec
}

func (s *planActSetup) lastCaptured(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(*s.captured) == 0 {
		t.Fatal("upstream was never called")
	}
	return (*s.captured)[len(*s.captured)-1]
}

func TestUnionActTargets(t *testing.T) {
	got := unionActTargets(
		[]string{"google/gemini-3-flash-preview", "qwen3.6-27b", "nvidia/nemotron-3.5-lightning-30b-a3b", "openai/gpt-oss-20b", "poolside/laguna-xs-2.1:free", "glm-4.5"},
		[]string{"google/gemini-3-flash-preview", "qwen3.6-27b", "openai/gpt-oss-20b", "nvidia/nemotron-3.5-lightning-30b-a3b", "poolside/laguna-xs-2.1:free", "glm-4.5", "fast-only-model"},
	)
	want := []string{"google/gemini-3-flash-preview", "qwen3.6-27b", "nvidia/nemotron-3.5-lightning-30b-a3b", "openai/gpt-oss-20b", "poolside/laguna-xs-2.1:free", "glm-4.5", "fast-only-model"}
	if len(got) != len(want) {
		t.Fatalf("union len %d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("union[%d] = %s want %s (full %v)", i, got[i], want[i], got)
		}
	}
	if got[0] != "google/gemini-3-flash-preview" {
		t.Fatalf("Kaggle must be first in Act union, got %s", got[0])
	}
	// Coder order wins over fast order: NVIDIA (coder pos 2) must
	// precede Groq (coder pos 3), even though fast puts Groq first.
	nvidiaIdx, groqIdx := -1, -1
	for i, m := range got {
		if strings.Contains(m, "nemotron") {
			nvidiaIdx = i
		}
		if m == "openai/gpt-oss-20b" {
			groqIdx = i
		}
	}
	if nvidiaIdx < 0 || groqIdx < 0 || nvidiaIdx > groqIdx {
		t.Fatalf("Act union must keep coder order NVIDIA before Groq, got %v", got)
	}
}

func TestPlanReasoningPool(t *testing.T) {
	s := newPlanActGateway(t)
	rec := s.doChat(t, "reasoning", `{"model":"reasoning","messages":[{"role":"user","content":"plan this change"}]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("Plan reasoning status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("x-gateway-pool") != "reasoning" {
		t.Fatalf("pool header = %q want reasoning", rec.Header().Get("x-gateway-pool"))
	}
	if got := s.lastCaptured(t); got != "google/gemini-3-flash-preview" {
		t.Fatalf("Plan first pick = %s want Kaggle (reasoning pos 0)", got)
	}
	// Pool isolation is proved by the alias setup itself: qwen is
	// in coder/fast but not in reasoning, so a reasoning request
	// can never select it through this pool (see the alias lists
	// in newPlanActGateway).
}

func TestActMergedPool(t *testing.T) {
	s := newPlanActGateway(t)
	rec := s.doChat(t, "act", `{"model":"act","messages":[{"role":"user","content":"do this"}]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("Act status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("x-gateway-pool") != "act" {
		t.Fatalf("pool header = %q want act", rec.Header().Get("x-gateway-pool"))
	}
	if got := s.lastCaptured(t); got != "google/gemini-3-flash-preview" {
		t.Fatalf("Act first pick = %s want Kaggle (union pos 0)", got)
	}
}

func TestActLargeGroqExcludedFirstPick(t *testing.T) {
	s := newPlanActGateway(t)
	big := strings.Repeat("a ", 50000) // ~100k chars, est > 32000 = large
	body := `{"model":"act","messages":[{"role":"user","content":"` + big + `"}]}`
	rec := s.doChat(t, "act", body, nil)
	if rec.Code != 200 {
		t.Fatalf("Act large status %d body %s", rec.Code, rec.Body.String())
	}
	if got := s.lastCaptured(t); got == "openai/gpt-oss-20b" {
		t.Fatal("large Act request must not first-pick Groq")
	} else if got != "google/gemini-3-flash-preview" {
		t.Fatalf("large Act first pick = %s want Kaggle", got)
	}
}

func TestActKaggleCap(t *testing.T) {
	// Rotator-level proof of the cap ported into Act: same Rotator
	// call HandleChat uses, with a billed map where Kaggle is 70%
	// of the pool. First pick must not be Kaggle; at 50% it may be.
	s := newPlanActGateway(t)
	routes, keys, aliases, cfg, err := s.gw.loadFleet()
	if err != nil {
		t.Fatal(err)
	}
	_ = aliases
	_ = cfg
	cls := scheduler.Classify(scheduler.ClassifyInput{Model: "act", Messages: []scheduler.Message{{Role: "user", Content: "hi"}}, PrivacyMode: "off", RequestID: "x"})
	// Build candidates via the Act union, as HandleChat does.
	union := unionActTargets(
		[]string{"google/gemini-3-flash-preview", "qwen3.6-27b", "nvidia/nemotron-3.5-lightning-30b-a3b", "openai/gpt-oss-20b", "poolside/laguna-xs-2.1:free", "glm-4.5"},
		[]string{"google/gemini-3-flash-preview", "qwen3.6-27b", "openai/gpt-oss-20b", "nvidia/nemotron-3.5-lightning-30b-a3b", "poolside/laguna-xs-2.1:free", "glm-4.5"},
	)
	enabled := []scheduler.Route{}
	for _, rt := range routes {
		if rt.Enabled && rt.RouteState != "disabled" {
			enabled = append(enabled, rt)
		}
	}
	cands := s.gw.selectCandidates(cls, "", union, enabled, keys, scheduler.SchedulerConfig{DispatchMode: "auto", WeightsJSON: `{"wSticky":0.35,"wRole":0.30,"wRel":0.20,"wCost":0.10,"wLat":0.05}`, RateLimitRPM: 120, PrivacyMode: "off"})
	if len(cands) == 0 {
		t.Fatal("no Act candidates")
	}
	billed := map[string]int{"k-kaggle": 700, "k-qwen": 100, "k-nvidia": 100, "k-groq": 50, "k-laguna": 25, "k-glm": 25}
	rot := scheduler.NewRotator()
	ordered := rot.ReorderForRotation(cands, "act", scheduler.SizeSmall, "google/gemini-3-flash-preview", billed)
	if ordered[0].Route.ModelID == "google/gemini-3-flash-preview" {
		t.Fatal("Kaggle at 70% share must be excluded from first-pick by the 60% cap")
	}
	billed["k-kaggle"] = 500
	billed["k-qwen"] = 500
	rot2 := scheduler.NewRotator()
	ordered2 := rot2.ReorderForRotation(cands, "act", scheduler.SizeSmall, "google/gemini-3-flash-preview", billed)
	if ordered2[0].Route.ModelID != "google/gemini-3-flash-preview" {
		t.Fatalf("Kaggle at 50%% share must remain first-pickable, got %s", ordered2[0].Route.ModelID)
	}
}

func TestImagePlanVisionOnly(t *testing.T) {
	s := newPlanActGateway(t)
	body := `{"model":"reasoning","messages":[{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`
	rec := s.doChat(t, "reasoning", body, nil)
	if rec.Code != 200 {
		t.Fatalf("Plan image status %d body %s", rec.Code, rec.Body.String())
	}
	got := s.lastCaptured(t)
	if got != "google/gemini-3-flash-preview" && got != "poolside/laguna-xs-2.1:free" {
		t.Fatalf("Plan image must go to a vision-capable route, got %s", got)
	}
	if rec.Header().Get("x-gateway-task-type") != "vision" {
		t.Fatalf("task-type header = %q want vision", rec.Header().Get("x-gateway-task-type"))
	}
}

func TestImageActVisionOnly(t *testing.T) {
	s := newPlanActGateway(t)
	body := `{"model":"act","messages":[{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`
	rec := s.doChat(t, "act", body, nil)
	if rec.Code != 200 {
		t.Fatalf("Act image status %d body %s", rec.Code, rec.Body.String())
	}
	got := s.lastCaptured(t)
	if got != "google/gemini-3-flash-preview" && got != "poolside/laguna-xs-2.1:free" {
		t.Fatalf("Act image must go to a vision-capable route, got %s", got)
	}
	if rec.Header().Get("x-gateway-pool") != "act" {
		t.Fatalf("pool header = %q want act", rec.Header().Get("x-gateway-pool"))
	}
}

func TestImageVisionUniversalExpansion(t *testing.T) {
	// Reasoning pool here has NO vision route at all; vision alias
	// has a vision-only route. An image Plan request must still
	// succeed via the vision pool expansion, not 503.
	s := newPlanActGateway(t)
	// Remove vision routes from the reasoning alias in this instance.
	for _, a := range []store.Alias{{ID: "a-reasoning2", Alias: "reasoning", TargetModels: []string{"openai/gpt-oss-20b", "nvidia/nemotron-3.5-lightning-30b-a3b"}}} {
		_ = s.mem.UpsertAlias(a)
	}
	_ = s.mem.UpsertAlias(store.Alias{ID: "a-vision2", Alias: "vision", TargetModels: []string{"vision-only-model"}})
	body := `{"model":"reasoning","messages":[{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`
	rec := s.doChat(t, "reasoning", body, nil)
	if rec.Code != 200 {
		t.Fatalf("Plan image with vision expansion status %d body %s (want 200 via vision-only route)", rec.Code, rec.Body.String())
	}
	if got := s.lastCaptured(t); got != "vision-only-model" {
		t.Fatalf("vision expansion must route to vision-only-model, got %s", got)
	}
}

func TestActSmallVsLarge(t *testing.T) {
	s := newPlanActGateway(t)
	small := s.doChat(t, "act", `{"model":"act","messages":[{"role":"user","content":"hi"}]}`, nil)
	if small.Code != 200 {
		t.Fatalf("Act small status %d", small.Code)
	}
	if got := s.lastCaptured(t); got != "google/gemini-3-flash-preview" {
		t.Fatalf("Act small first pick = %s want Kaggle", got)
	}
	// Large is proved separately in TestActLargeGroqExcludedFirstPick;
	// here assert the size classes themselves, so the split is not
	// accidentally re-tuned without a test failing.
	if scheduler.SizeClassOf(1000) != scheduler.SizeSmall {
		t.Fatal("1000 est must be small")
	}
	if scheduler.SizeClassOf(40000) != scheduler.SizeLarge {
		t.Fatal("40000 est must be large")
	}
}

func TestActDirectPinOverride(t *testing.T) {
	s := newPlanActGateway(t)
	rec := s.doChat(t, "act", `{"model":"act","messages":[{"role":"user","content":"do this"}]}`, map[string]string{"X-Portal-Target-Model": "qwen3.6-27b"})
	if rec.Code != 200 {
		t.Fatalf("direct-pin status %d body %s", rec.Code, rec.Body.String())
	}
	if got := s.lastCaptured(t); got != "qwen3.6-27b" {
		t.Fatalf("direct-pin must select qwen, got %s", got)
	}
	if rec.Header().Get("x-gateway-pool") != "qwen3.6-27b" {
		t.Fatalf("pool header = %q want qwen3.6-27b (direct pin)", rec.Header().Get("x-gateway-pool"))
	}
}

func TestActOperatorOverride(t *testing.T) {
	s := newPlanActGateway(t)
	_ = s.mem.UpsertAlias(store.Alias{ID: "a-act", Alias: "act", TargetModels: []string{"glm-4.5", "google/gemini-3-flash-preview"}})
	rec := s.doChat(t, "act", `{"model":"act","messages":[{"role":"user","content":"do this"}]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("operator act status %d body %s", rec.Code, rec.Body.String())
	}
	if got := s.lastCaptured(t); got != "glm-4.5" {
		t.Fatalf("operator act alias must win over synthetic union, got %s", got)
	}
}
