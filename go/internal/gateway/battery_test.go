// Phase 6 battery: Cline-shaped end-to-end cases through HandleChat
// against mock upstreams only. No live provider, no production DB, no
// real key is involved anywhere in this file. The binary-level battery
// (go/battery_smoke.py) replays the same shapes against the built
// binary with a throwaway SQLite file.
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wxm0394/Switchyard/internal/crypto"
	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
	"github.com/wxm0394/Switchyard/internal/upstream"
)

type batterySpec struct {
	id, model, base string
	role            string
	vision, tools   bool
	maxCtx          int
	allowSens       bool
	privOnly        bool
	reliability     int
}

func batteryGateway(t *testing.T, specs []batterySpec, aliasTargets []string) (*Gateway, *store.MemStore) {
	t.Helper()
	mem := store.NewMemStore()
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 7)
	}
	visionModels := []string{}
	for _, s := range specs {
		enc, err := crypto.Encrypt(keyBytes, "battery-test-key-"+s.id)
		if err != nil {
			t.Fatal(err)
		}
		_ = mem.UpsertKey(scheduler.ProviderKey{ID: "k-" + s.id, Name: "Battery " + s.id, ProviderType: "openai-compatible", EncryptedKey: enc, KeyMask: "battery••••key", KeyState: "healthy", BaseURL: s.base})
		role := s.role
		if role == "" {
			role = "auto"
		}
		maxCtx := s.maxCtx
		if maxCtx == 0 {
			maxCtx = 128000
		}
		rel := s.reliability
		if rel == 0 {
			rel = 100
		}
		_ = mem.UpsertRoute(scheduler.Route{ID: "r-" + s.id, Name: s.model, ProviderKeyID: "k-" + s.id, ModelID: s.model, DisplayName: s.model, BaseURL: s.base, ProviderType: "openai-compatible", UpstreamModel: s.model, TaskRole: role, RouteState: "healthy", CircuitState: "closed", SupportsVision: s.vision, SupportsTools: s.tools, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: maxCtx, AllowSensitive: s.allowSens, PrivateOnly: s.privOnly, ReliabilityScore: rel, CostWeight: 1, Enabled: true})
		if s.vision {
			visionModels = append(visionModels, s.model)
		}
	}
	for _, alias := range []string{"coder", "fast", "reasoning"} {
		_ = mem.UpsertAlias(store.Alias{ID: "a-" + alias, Alias: alias, TargetModels: aliasTargets, Description: alias})
	}
	if len(visionModels) > 0 {
		_ = mem.UpsertAlias(store.Alias{ID: "a-vision", Alias: "vision", TargetModels: visionModels, Description: "vision"})
	}
	_ = mem.UpsertToken(store.ClientToken{ID: "tb", Name: "battery", Token: "sk-portal-battery", CreatedAt: "2026-01-01T00:00:00Z", Active: true})
	gw := New(mem, Config{MasterKey: keyBytes, Decrypt: func(enc string) (string, error) { return crypto.Decrypt(keyBytes, enc) }})
	return gw, mem
}

func batteryPost(t *testing.T, gw *Gateway, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-portal-battery")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, req)
	return rec
}

func batteryMessage(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var parsed map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("response is not JSON: %v body %s", err, rec.Body.String())
	}
	choices, ok := parsed["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		t.Fatalf("no choices in response: %s", rec.Body.String())
	}
	msg, ok := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	if !ok {
		t.Fatalf("no message in response: %s", rec.Body.String())
	}
	return msg
}

func batteryFinish(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var parsed map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	choices := parsed["choices"].([]interface{})
	return choices[0].(map[string]interface{})["finish_reason"].(string)
}

func batteryToolBody(content, toolName string) string {
	return fmt.Sprintf(`{"model":"coder","messages":[{"role":"user","content":%q}],"tools":[{"type":"function","function":{"name":%q,"parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`,
		content, toolName)
}

// Plain upstream: always answers with the given body, recording how
// many times it was hit and the last request body length.
type batteryUpstream struct {
	server  *httptest.Server
	hits    atomic.Int32
	lastLen atomic.Int32
	body    string
}

func newBatteryUpstream(t *testing.T, body string) *batteryUpstream {
	t.Helper()
	b := &batteryUpstream{body: body}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		b.lastLen.Store(int32(buf.Len()))
		b.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(b.body))
	}))
	return b
}

func plainOK(content string, tokens int) string {
	m := map[string]interface{}{
		"id": "chatcmpl-battery",
		"choices": []interface{}{map[string]interface{}{
			"message":       map[string]interface{}{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]interface{}{"total_tokens": tokens},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestBatteryRescueDialects(t *testing.T) {
	dialects := []struct {
		name    string
		content string
	}{
		{"kimi", `<|tool_call_begin|> functions.get_weather:0 <|tool_call_argument_begin|> {"city": "Tokyo"} <|tool_call_end|>`},
		{"llama", `<function=get_weather>{"city": "Tokyo"}</function>`},
		{"qwen-xml", `<tool_call>{"name": "get_weather", "arguments": {"city": "Tokyo"}}</tool_call>`},
		{"bare-json", `{"name": "get_weather", "arguments": {"city": "Tokyo"}}`},
		{"fenced-json", "```json\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Tokyo\"}}\n```"},
	}
	for _, d := range dialects {
		t.Run(d.name, func(t *testing.T) {
			up := newBatteryUpstream(t, plainOK(d.content, 50))
			defer up.server.Close()
			gw, _ := batteryGateway(t, []batterySpec{{id: "m1", model: "test-model", base: up.server.URL + "/v1", tools: true}}, []string{"test-model"})
			rec := batteryPost(t, gw, batteryToolBody("weather?", "get_weather"), nil)
			if rec.Code != 200 {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			msg := batteryMessage(t, rec)
			if msg["content"] != d.content {
				t.Fatalf("original content must be kept, got %v", msg["content"])
			}
			if batteryFinish(t, rec) != "tool_calls" {
				t.Fatalf("finish_reason = %v, want tool_calls", batteryFinish(t, rec))
			}
			tc, ok := msg["tool_calls"].([]interface{})
			if !ok || len(tc) != 1 {
				t.Fatalf("expected 1 rescued tool_call, got %v", msg["tool_calls"])
			}
			fn := tc[0].(map[string]interface{})["function"].(map[string]interface{})
			if fn["name"] != "get_weather" {
				t.Fatalf("rescued name = %v", fn["name"])
			}
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil || args["city"] != "Tokyo" {
				t.Fatalf("rescued arguments = %v err %v", fn["arguments"], err)
			}
		})
	}
}

func TestBatteryRescueRefusals(t *testing.T) {
	// Undeclared tool name: no rescue, plain text passes through.
	up := newBatteryUpstream(t, plainOK(`<function=delete_file>{"path": "/tmp/x"}</function>`, 50))
	defer up.server.Close()
	gw, _ := batteryGateway(t, []batterySpec{{id: "m1", model: "test-model", base: up.server.URL + "/v1", tools: true}}, []string{"test-model"})
	rec := batteryPost(t, gw, batteryToolBody("weather?", "get_weather"), nil)
	if rec.Code != 200 {
		t.Fatalf("undeclared status %d body %s", rec.Code, rec.Body.String())
	}
	if _, has := batteryMessage(t, rec)["tool_calls"]; has {
		t.Fatalf("undeclared tool must not be rescued: %s", rec.Body.String())
	}
	// Majority gate: a long explanation with one small tag is an
	// example, not a call.
	long := strings.Repeat("This is a long explanation of how tool calls work. ", 100)
	content := long + `<function=get_weather>{"city": "Tokyo"}</function>`
	up2 := newBatteryUpstream(t, plainOK(content, 50))
	defer up2.server.Close()
	gw2, _ := batteryGateway(t, []batterySpec{{id: "m1", model: "test-model", base: up2.server.URL + "/v1", tools: true}}, []string{"test-model"})
	rec2 := batteryPost(t, gw2, batteryToolBody("weather?", "get_weather"), nil)
	if rec2.Code != 200 {
		t.Fatalf("majority status %d body %s", rec2.Code, rec2.Body.String())
	}
	if _, has := batteryMessage(t, rec2)["tool_calls"]; has {
		t.Fatalf("minority tag must not be rescued: %s", rec2.Body.String())
	}
}

func toolCallResponse(name, args string) string {
	m := map[string]interface{}{
		"id": "chatcmpl-battery",
		"choices": []interface{}{map[string]interface{}{
			"message": map[string]interface{}{
				"role": "assistant", "content": "",
				"tool_calls": []interface{}{map[string]interface{}{
					"id": "call_1", "type": "function",
					"function": map[string]interface{}{"name": name, "arguments": args},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]interface{}{"total_tokens": 50},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func searchToolBody() string {
	return `{"model":"coder","messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}},"filter":{"type":"object","properties":{"city":{"type":"string"}}},"note":{"type":"string"},"count":{"type":"integer"}}}}}]}`
}

func batteryArgs(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	msg := batteryMessage(t, rec)
	tc := msg["tool_calls"].([]interface{})
	return tc[0].(map[string]interface{})["function"].(map[string]interface{})["arguments"].(string)
}

func TestBatteryToolArgs(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string // "" means: arguments must come back unchanged
	}{
		{"array-double", `{"tags": "[\"a\", \"b\"]"}`, `{"tags":["a","b"]}`},
		{"object-double", `{"filter": "{\"city\": \"Tokyo\"}"}`, `{"filter":{"city":"Tokyo"}}`},
		{"whole-args-double", `"{\"tags\": [\"a\"]}"`, `{"tags":["a"]}`},
		{"string-param-untouched", `{"note": "[\"a\"]"}`, ``},
		{"unknown-key-untouched", `{"mystery": "[\"x\"]", "tags": ["a"]}`, ``},
		{"scalar-untouched", `{"count": "5"}`, ``},
		{"triple-untouched", `{"tags": "\"[\\\"a\\\"]\""}`, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up := newBatteryUpstream(t, toolCallResponse("search", c.args))
			defer up.server.Close()
			gw, _ := batteryGateway(t, []batterySpec{{id: "m1", model: "test-model", base: up.server.URL + "/v1", tools: true}}, []string{"test-model"})
			rec := batteryPost(t, gw, searchToolBody(), nil)
			if rec.Code != 200 {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			got := batteryArgs(t, rec)
			if c.want == "" {
				if got != c.args {
					t.Fatalf("arguments must be untouched, got %v want %v", got, c.args)
				}
			} else if got != c.want {
				t.Fatalf("arguments = %v want %v", got, c.want)
			}
		})
	}
}

func TestBatteryLargeHistory(t *testing.T) {
	// ~162,000 ASCII chars -> estimated ceil(162000*0.36*1.25) = 72,900
	// tokens, well over the 70k Cline shape and over the 32,000 large
	// threshold. Groq must not be first-picked for a large request,
	// and a 32k-context route must be filtered out entirely.
	big := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 3600)
	if len(big) < 150000 {
		t.Fatalf("large fixture too small: %d", len(big))
	}
	normal := newBatteryUpstream(t, plainOK("ok", 100))
	defer normal.server.Close()
	small := newBatteryUpstream(t, plainOK("ok-small", 100))
	defer small.server.Close()
	gw, mem := batteryGateway(t, []batterySpec{
		{id: "groq", model: "groq-model", base: "https://api.groq.com/openai/v1", tools: true},
		{id: "normal", model: "normal-model", base: normal.server.URL + "/v1", tools: true},
		{id: "small", model: "small-model", base: small.server.URL + "/v1", tools: true, maxCtx: 32000},
	}, []string{"groq-model", "normal-model", "small-model"})
	body, _ := json.Marshal(map[string]interface{}{
		"model": "coder",
		"messages": []interface{}{
			map[string]interface{}{"role": "system", "content": "You are a coding agent."},
			map[string]interface{}{"role": "user", "content": big},
		},
	})
	rec := batteryPost(t, gw, string(body), nil)
	if rec.Code != 200 {
		t.Fatalf("large status %d body %s", rec.Code, rec.Body.String()[:500])
	}
	if normal.lastLen.Load() < 150000 {
		t.Fatalf("upstream must receive the full large body, got %d bytes", normal.lastLen.Load())
	}
	logs, _ := mem.ListAudit(10, 0)
	if len(logs) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(logs))
	}
	if logs[0].EstimatedTokens <= 70000 {
		t.Fatalf("estimated tokens = %d, want > 70000", logs[0].EstimatedTokens)
	}
	if logs[0].SelectedModelID != "normal-model" {
		t.Fatalf("large first pick = %s, want normal-model (Groq excluded, 32k route filtered)", logs[0].SelectedModelID)
	}
	if small.hits.Load() != 0 {
		t.Fatalf("32k-context route must never serve a large request, hits = %d", small.hits.Load())
	}
}

func TestBatteryStreamingUntouched(t *testing.T) {
	// Streaming body already carries tool_calls with double-encoded
	// args AND a garbled tag in content: both must pass through
	// byte-identical, no rescue, no args repair. The fixture is real
	// SSE: a stream:true request never gets a plain JSON object back
	// from an OpenAI-compatible upstream, and the SSE content check
	// treats a non-SSE body as empty.
	mockJSON := toolCallResponse("search", `{"tags": "[\"a\"]"}`)
	// Replace content with a garbled tag as well.
	var parsed map[string]interface{}
	_ = json.Unmarshal([]byte(mockJSON), &parsed)
	msg := parsed["choices"].([]interface{})[0].(map[string]interface{})["message"].(map[string]interface{})
	msg["content"] = `<function=search>{"tags": ["a"]}</function>`
	delta, _ := json.Marshal(map[string]interface{}{
		"id": "chatcmpl-battery",
		"choices": []interface{}{map[string]interface{}{
			"delta": msg, "finish_reason": nil,
		}},
	})
	finish, _ := json.Marshal(map[string]interface{}{
		"id": "chatcmpl-battery",
		"choices": []interface{}{map[string]interface{}{
			"delta": map[string]interface{}{}, "finish_reason": "tool_calls",
		}},
	})
	mockBody := "data: " + string(delta) + "\n\ndata: " + string(finish) + "\n\ndata: [DONE]\n"
	up := newBatteryUpstream(t, mockBody)
	defer up.server.Close()
	gw, _ := batteryGateway(t, []batterySpec{{id: "m1", model: "test-model", base: up.server.URL + "/v1", tools: true}}, []string{"test-model"})
	body := `{"model":"coder","stream":true,"messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}}}}}}]}`
	rec := batteryPost(t, gw, body, nil)
	if rec.Code != 200 {
		t.Fatalf("streaming status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != mockBody {
		t.Fatalf("streaming body must be byte-identical.\n got: %s\nwant: %s", rec.Body.String(), mockBody)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("streaming content-type = %q, want text/event-stream", ct)
	}
}

func TestBatteryPrivacyModes(t *testing.T) {
	open := batterySpec{id: "open", model: "open-model", base: "http://example.invalid/v1", tools: true}
	secret := batterySpec{id: "secret", model: "secret-model", base: "http://example.invalid/v1", tools: true, allowSens: true}
	secretary := batterySpec{id: "sec", model: "secretary-model", base: "http://example.invalid/v1", tools: true, allowSens: true, privOnly: true}
	cases := []struct {
		name    string
		mode    string
		content string
		header  bool // X-Router-Private: true
		specs   []batterySpec
		targets []string
		want200 bool
	}{
		{"off-ignores-secret-words", "off", "my api_secret is xyz", false, []batterySpec{open}, []string{"open-model"}, true},
		{"low-secret-words-private", "low", "my api_secret is xyz", false, []batterySpec{open}, []string{"open-model"}, false},
		{"low-secret-words-allowed-route", "low", "my api_secret is xyz", false, []batterySpec{secret}, []string{"secret-model"}, true},
		{"low-exchange-not-private", "low", "binance trading notes", false, []batterySpec{open}, []string{"open-model"}, true},
		{"high-exchange-private", "high", "binance trading notes", false, []batterySpec{open}, []string{"open-model"}, false},
		{"high-header-private", "high", "hello", true, []batterySpec{open}, []string{"open-model"}, false},
		{"secretary-excluded-when-not-private", "low", "hello", false, []batterySpec{secretary}, []string{"secretary-model"}, false},
		{"secretary-serves-private", "low", "my api_secret is xyz", false, []batterySpec{secretary}, []string{"secretary-model"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up := newBatteryUpstream(t, plainOK("ok", 10))
			defer up.server.Close()
			specs := make([]batterySpec, len(c.specs))
			copy(specs, c.specs)
			for i := range specs {
				specs[i].base = up.server.URL + "/v1"
			}
			gw, mem := batteryGateway(t, specs, c.targets)
			_, _ = mem.UpdateConfig(map[string]interface{}{"privacyMode": c.mode})
			headers := map[string]string{}
			if c.header {
				headers["X-Router-Private"] = "true"
			}
			body, _ := json.Marshal(map[string]interface{}{
				"model":    "coder",
				"messages": []interface{}{map[string]interface{}{"role": "user", "content": c.content}},
			})
			rec := batteryPost(t, gw, string(body), headers)
			if c.want200 && rec.Code != 200 {
				t.Fatalf("want 200, got %d body %s", rec.Code, rec.Body.String())
			}
			if !c.want200 {
				if rec.Code != 503 {
					t.Fatalf("want 503 no_healthy_route, got %d body %s", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "no_healthy_route") {
					t.Fatalf("503 body must carry no_healthy_route: %s", rec.Body.String())
				}
				logs, _ := mem.ListAudit(10, 0)
				if len(logs) != 0 {
					t.Fatalf("503 must not write audit, got %d rows", len(logs))
				}
			}
		})
	}
	// The documented divergence, pinned as a test so it can only
	// change deliberately: missing/invalid mode maps to off in Go,
	// where Node falls back to high. Migrated DBs storing "off" are
	// unaffected; this is a review item, not a silent change.
	if got := scheduler.EffectivePrivacyMode(""); got != "off" {
		t.Fatalf("empty privacy mode = %q, want off (documented divergence)", got)
	}
	if got := scheduler.EffectivePrivacyMode("bogus"); got != "off" {
		t.Fatalf("invalid privacy mode = %q, want off (documented divergence)", got)
	}
}

func TestBatteryRotationDistribution(t *testing.T) {
	up := newBatteryUpstream(t, plainOK("ok", 10))
	defer up.server.Close()
	gw, mem := batteryGateway(t, []batterySpec{
		{id: "m1", model: "m1", base: up.server.URL + "/v1", tools: true},
		{id: "m2", model: "m2", base: up.server.URL + "/v1", tools: true},
		{id: "m3", model: "m3", base: up.server.URL + "/v1", tools: true},
	}, []string{"m1", "m2", "m3"})
	seq := []string{}
	for i := 0; i < 10; i++ {
		rec := batteryPost(t, gw, `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`, nil)
		if rec.Code != 200 {
			t.Fatalf("request %d status %d body %s", i, rec.Code, rec.Body.String())
		}
		logs, _ := mem.ListAudit(1, 0)
		if len(logs) != 1 {
			t.Fatalf("request %d: no audit row", i)
		}
		seq = append(seq, logs[0].SelectedModelID)
	}
	// Smooth weighted round-robin, weights 4/3/2, over 10 picks:
	// m1,m2,m3,m1,m2,m1,m3,m2,m1,m1 -> counts 5/3/2.
	want := []string{"m1", "m2", "m3", "m1", "m2", "m1", "m3", "m2", "m1", "m1"}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("rotation sequence = %v, want %v", seq, want)
		}
	}
}

func TestBatteryKaggleCap(t *testing.T) {
	r := scheduler.NewRotator()
	kaggle := scheduler.Candidate{Route: scheduler.Route{ID: "rk", ModelID: "kaggle-model"}, Key: scheduler.ProviderKey{ID: "kk"}}
	other := scheduler.Candidate{Route: scheduler.Route{ID: "ro", ModelID: "other-model"}, Key: scheduler.ProviderKey{ID: "ko"}}
	// 87.5% Kaggle share > 60% cap: Kaggle must not be first-picked.
	out := r.ReorderForRotation([]scheduler.Candidate{kaggle, other}, "coder", scheduler.SizeSmall, "kaggle-model", map[string]int{"kk": 7000, "ko": 1000})
	if out[0].Route.ID != "ro" {
		t.Fatalf("over-cap first pick = %s, want other route", out[0].Route.ID)
	}
	// 50% share: cap does not apply, weight 4 beats weight 3.
	r2 := scheduler.NewRotator()
	out2 := r2.ReorderForRotation([]scheduler.Candidate{kaggle, other}, "coder", scheduler.SizeSmall, "kaggle-model", map[string]int{"kk": 5000, "ko": 5000})
	if out2[0].Route.ID != "rk" {
		t.Fatalf("under-cap first pick = %s, want kaggle route", out2[0].Route.ID)
	}
}

func TestBatteryCircuitBreaker(t *testing.T) {
	// Rotation would interleave first-picks across a two-route alias,
	// so the breaker is driven in isolation first (single-route alias,
	// then direct-pin), and only then combined with failover. A first
	// version of this test expected 3 flaky hits over 4 alias requests
	// and got 2, because rotation sent request 2 straight to stable;
	// that was the test's fault, not the breaker's.
	var flakyHits, stableHits atomic.Int32
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flakyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"boom","code":"server_error"}}`))
	}))
	defer flaky.Close()
	stable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stableHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(plainOK("ok", 10)))
	}))
	defer stable.Close()
	// (a) Single-route alias: 3 failures open the breaker, the 4th
	// request finds no candidate at all.
	gw, mem := batteryGateway(t, []batterySpec{
		{id: "flaky", model: "flaky-model", base: flaky.URL + "/v1", tools: true},
	}, []string{"flaky-model"})
	for i := 0; i < 3; i++ {
		rec := batteryPost(t, gw, `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`, nil)
		if rec.Code != 502 {
			t.Fatalf("failure %d status %d, want 502", i, rec.Code)
		}
	}
	if flakyHits.Load() != 3 {
		t.Fatalf("flaky hits = %d, want 3", flakyHits.Load())
	}
	routes, _ := mem.ListRoutes()
	for _, rt := range routes {
		if rt.ID == "r-flaky" && rt.CircuitState != "open" {
			t.Fatalf("flaky circuit state = %q, want open", rt.CircuitState)
		}
	}
	rec := batteryPost(t, gw, `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "no_healthy_route") {
		t.Fatalf("open breaker status %d body %s, want 503 no_healthy_route", rec.Code, rec.Body.String())
	}
	if flakyHits.Load() != 3 {
		t.Fatalf("open breaker must not retry upstream, hits = %d", flakyHits.Load())
	}
	// (b) Breaker + failover: drive the same route open via direct-pin,
	// then an alias request must go straight to stable.
	gw2, _ := batteryGateway(t, []batterySpec{
		{id: "flaky", model: "flaky-model", base: flaky.URL + "/v1", tools: true},
		{id: "stable", model: "stable-model", base: stable.URL + "/v1", tools: true},
	}, []string{"flaky-model", "stable-model"})
	for i := 0; i < 3; i++ {
		rec := batteryPost(t, gw2, `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`, map[string]string{"X-Portal-Target-Model": "flaky-model"})
		if rec.Code != 502 {
			t.Fatalf("pinned failure %d status %d, want 502", i, rec.Code)
		}
	}
	if flakyHits.Load() != 6 {
		t.Fatalf("flaky hits after pinned failures = %d, want 6", flakyHits.Load())
	}
	rec = batteryPost(t, gw2, `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("alias after breaker open status %d body %s, want 200 via stable", rec.Code, rec.Body.String())
	}
	if flakyHits.Load() != 6 {
		t.Fatalf("alias request must not retry the open route, hits = %d", flakyHits.Load())
	}
	if stableHits.Load() != 1 {
		t.Fatalf("stable hits = %d, want 1", stableHits.Load())
	}
}

func TestBatteryTimeoutTiers(t *testing.T) {
	if got := upstream.TimeoutTierFor("r1", "Kaggle GPU Qwen", "qwen3.6-27b", "qwen3.5-9b", "https://llm.example.com/v1", "auto", false); got.TimeoutMs != 120000 || !got.IsColdStart {
		t.Fatalf("cold-start tier = %+v, want 120000/cold_start", got)
	}
	if got := upstream.TimeoutTierFor("r2", "Fast", "fast-model", "fast-model", "https://api.example.com/v1", "fast", true); got.TimeoutMs != 45000 {
		t.Fatalf("fast streaming tier = %+v, want 45000", got)
	}
	if got := upstream.TimeoutTierFor("r2", "Fast", "fast-model", "fast-model", "https://api.example.com/v1", "fast", false); got.TimeoutMs != 60000 {
		t.Fatalf("fast non-streaming tier = %+v, want 60000", got)
	}
	if got := upstream.TimeoutTierFor("r3", "Reason", "reason-model", "reason-model", "https://api.example.com/v1", "auto", true); got.TimeoutMs != 45000 {
		t.Fatalf("reasoning streaming tier = %+v, want 45000", got)
	}
	if got := upstream.TimeoutTierFor("r3", "Reason", "reason-model", "reason-model", "https://api.example.com/v1", "auto", false); got.TimeoutMs != 60000 {
		t.Fatalf("reasoning non-streaming tier = %+v, want 60000", got)
	}
	// Direct callUpstream timeout: a 2s-slow mock with a 200ms client
	// timeout must surface as CategoryTimeout (this is the same code
	// path HandleChat uses; sleeping a full 60s tier here would prove
	// nothing extra, so the tier values above plus this error path
	// are the honest coverage).
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(plainOK("slow", 10)))
	}))
	defer slow.Close()
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 7)
	}
	enc, err := crypto.Encrypt(keyBytes, "battery-timeout-key")
	if err != nil {
		t.Fatal(err)
	}
	mem := store.NewMemStore()
	gw := New(mem, Config{MasterKey: keyBytes, Decrypt: func(e string) (string, error) { return crypto.Decrypt(keyBytes, e) }})
	cand := scheduler.Candidate{
		Route: scheduler.Route{ID: "r-slow", ModelID: "slow-model", UpstreamModel: "slow-model", BaseURL: slow.URL + "/v1"},
		Key:   scheduler.ProviderKey{ID: "k-slow", EncryptedKey: enc},
	}
	_, _, _, uerr := gw.callUpstream(cand, []byte(`{"model":"slow-model","messages":[]}`), nil, false, 200)
	if uerr == nil || uerr.Category != upstream.CategoryTimeout {
		t.Fatalf("slow upstream with 200ms timeout: err = %+v, want CategoryTimeout", uerr)
	}
}

func TestBatteryDirectPin(t *testing.T) {
	up := newBatteryUpstream(t, plainOK("ok", 10))
	defer up.server.Close()
	gw, mem := batteryGateway(t, []batterySpec{
		{id: "alias", model: "alias-model", base: up.server.URL + "/v1", tools: true},
		{id: "pinned", model: "pinned-model", base: up.server.URL + "/v1", tools: true},
	}, []string{"alias-model"})
	for i := 0; i < 3; i++ {
		rec := batteryPost(t, gw, `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`, map[string]string{"X-Portal-Target-Model": "pinned-model"})
		if rec.Code != 200 {
			t.Fatalf("pinned request %d status %d body %s", i, rec.Code, rec.Body.String())
		}
		logs, _ := mem.ListAudit(1, 0)
		if len(logs) != 1 || logs[0].SelectedModelID != "pinned-model" {
			t.Fatalf("pinned request %d audit = %+v, want pinned-model", i, logs)
		}
	}
}

func TestBatteryAuthAndValidation(t *testing.T) {
	up := newBatteryUpstream(t, plainOK("ok", 10))
	defer up.server.Close()
	gw, mem := batteryGateway(t, []batterySpec{{id: "m1", model: "test-model", base: up.server.URL + "/v1", tools: true}}, []string{"test-model"})
	// Wrong token -> 401.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"coder","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-portal-wrong")
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, req)
	if rec.Code != 401 {
		t.Fatalf("wrong token status %d, want 401", rec.Code)
	}
	// Disabled token -> 403.
	_ = mem.UpsertToken(store.ClientToken{ID: "td", Name: "disabled", Token: "sk-portal-disabled", CreatedAt: "2026-01-01T00:00:00Z", Active: false})
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"coder","messages":[{"role":"user","content":"hi"}]}`))
	req2.Header.Set("Authorization", "Bearer sk-portal-disabled")
	rec2 := httptest.NewRecorder()
	gw.HandleChat(rec2, req2)
	if rec2.Code != 403 {
		t.Fatalf("disabled token status %d, want 403", rec2.Code)
	}
	// Empty content -> validator fail_empty_content -> 502, no audit.
	empty := newBatteryUpstream(t, `{"id":"chatcmpl-e","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"total_tokens":5}}`)
	defer empty.server.Close()
	gw2, mem2 := batteryGateway(t, []batterySpec{{id: "m1", model: "test-model", base: empty.server.URL + "/v1", tools: true}}, []string{"test-model"})
	rec3 := batteryPost(t, gw2, `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec3.Code != 502 || !strings.Contains(rec3.Body.String(), "fail_empty_content") {
		t.Fatalf("empty content status %d body %s, want 502 fail_empty_content", rec3.Code, rec3.Body.String())
	}
	logs, _ := mem2.ListAudit(10, 0)
	if len(logs) != 0 {
		t.Fatalf("validator failure must not write audit, got %d rows", len(logs))
	}
	// Length cut-off with short content -> fail_truncated_length -> 502.
	trunc := newBatteryUpstream(t, `{"id":"chatcmpl-t","choices":[{"message":{"role":"assistant","content":"short"},"finish_reason":"length"}],"usage":{"total_tokens":5}}`)
	defer trunc.server.Close()
	gw3, _ := batteryGateway(t, []batterySpec{{id: "m1", model: "test-model", base: trunc.server.URL + "/v1", tools: true}}, []string{"test-model"})
	rec4 := batteryPost(t, gw3, `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec4.Code != 502 || !strings.Contains(rec4.Body.String(), "fail_truncated_length") {
		t.Fatalf("truncated status %d body %s, want 502 fail_truncated_length", rec4.Code, rec4.Body.String())
	}
}

func TestBatteryAuditTimestampPrecision(t *testing.T) {
	// Phase 6: audit timestamps must carry milliseconds (Node's
	// toISOString shape). Second precision made same-second bursts
	// indistinguishable in the log.
	up := newBatteryUpstream(t, plainOK("ok", 10))
	defer up.server.Close()
	gw, mem := batteryGateway(t, []batterySpec{{id: "m1", model: "test-model", base: up.server.URL + "/v1", tools: true}}, []string{"test-model"})
	rec := batteryPost(t, gw, `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	logs, _ := mem.ListAudit(1, 0)
	if len(logs) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(logs))
	}
	ts := logs[0].Timestamp
	if len(ts) < 24 || ts[19] != '.' {
		t.Fatalf("audit timestamp = %q, want millisecond precision (YYYY-MM-DDTHH:MM:SS.mmmZ)", ts)
	}
}
