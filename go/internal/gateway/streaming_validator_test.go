package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wxm0394/Switchyard/internal/crypto"
	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
)

func TestSSEStreamContent(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantHas   bool
		wantUsage int
	}{
		{"empty body", "", false, 0},
		{"done only", "data: [DONE]\n", false, 0},
		{"role-only deltas", "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n", false, 0},
		{"whitespace content", "data: {\"choices\":[{\"delta\":{\"content\":\"   \"}}]}\n\ndata: [DONE]\n", false, 0},
		{"content delta", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n", true, 0},
		{"tool_call delta", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\"}]}}]}\n\ndata: [DONE]\n", true, 0},
		{"usage chunk with content", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n", true, 15},
		{"usage chunk alone", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n", false, 15},
		{"usage without total", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\ndata: [DONE]\n", true, 15},
		{"malformed line skipped", "data: not-json\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n", true, 0},
		{"non-sse body", "{\"choices\":[{\"message\":{\"content\":\"hi\"}}]}", false, 0},
		{"comment and blank lines", ": ping\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			has, usage := sseStreamContent([]byte(tc.body))
			if has != tc.wantHas || usage != tc.wantUsage {
				t.Fatalf("sseStreamContent = (%v, %d), want (%v, %d)", has, usage, tc.wantHas, tc.wantUsage)
			}
		})
	}
}

const (
	emptySSEBody = "data: {\"id\":\"chatcmpl-e\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"id\":\"chatcmpl-e\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n"
	goodSSEBody  = "data: {\"id\":\"chatcmpl-g\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"4\"}}]}\n\ndata: {\"id\":\"chatcmpl-g\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n"
	usageSSEBody = "data: {\"id\":\"chatcmpl-u\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"4\"}}]}\n\ndata: {\"id\":\"chatcmpl-u\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n"
)

func sseUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
}

// Two routes on one key, alias coder -> [model-empty, model-good].
// Alias order plus position-based rotation weights make the empty
// route the first candidate on a fresh gateway.
func testGatewayTwoRoutes(t *testing.T, firstURL, secondURL string) (*Gateway, *store.MemStore) {
	t.Helper()
	mem := store.NewMemStore()
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 7)
	}
	enc, err := crypto.Encrypt(keyBytes, "upstream-test-key")
	if err != nil {
		t.Fatal(err)
	}
	_ = mem.UpsertKey(scheduler.ProviderKey{ID: "k1", Name: "Test", ProviderType: "openai-compatible", EncryptedKey: enc, KeyMask: "test••••key", KeyState: "healthy", BaseURL: firstURL})
	route := func(id, model, base string) scheduler.Route {
		return scheduler.Route{ID: id, Name: model, ProviderKeyID: "k1", ModelID: model, DisplayName: model, BaseURL: base, ProviderType: "openai-compatible", UpstreamModel: model, TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsVision: true, SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: 128000, ReliabilityScore: 100, CostWeight: 1, Enabled: true}
	}
	_ = mem.UpsertRoute(route("r1", "model-empty", firstURL))
	_ = mem.UpsertRoute(route("r2", "model-good", secondURL))
	_ = mem.UpsertAlias(store.Alias{ID: "a1", Alias: "coder", TargetModels: []string{"model-empty", "model-good"}, Description: "coder"})
	_ = mem.UpsertToken(store.ClientToken{ID: "t1", Name: "test", Token: "sk-portal-test", CreatedAt: "2026-01-01T00:00:00Z", Active: true})
	gw := New(mem, Config{MasterKey: keyBytes, Decrypt: func(enc string) (string, error) { return crypto.Decrypt(keyBytes, enc) }})
	return gw, mem
}

func streamChatRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-portal-test")
	return req
}

// Empty SSE on the first candidate must fail over to the second one:
// the client sees the good stream, and the audit row is honest about
// it (unverified, estimate, chain carries fail_empty_content).
func TestChatStreamingEmptyFailover(t *testing.T) {
	empty := sseUpstream(t, emptySSEBody)
	defer empty.Close()
	good := sseUpstream(t, goodSSEBody)
	defer good.Close()
	gw, mem := testGatewayTwoRoutes(t, empty.URL+"/v1", good.URL+"/v1")
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, streamChatRequest(`{"model":"coder","stream":true,"messages":[{"role":"user","content":"2+2?"}]}`))
	if rec.Code != 200 {
		t.Fatalf("chat status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"4"`) {
		t.Fatalf("failover must serve the good stream: %s", rec.Body.String())
	}
	logs, _ := mem.ListAudit(10, 0)
	if len(logs) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(logs))
	}
	row := logs[0]
	if row.SelectedRouteID != "r2" {
		t.Fatalf("audit route = %s, want r2", row.SelectedRouteID)
	}
	if row.ValidatorResult != "unverified" {
		t.Fatalf("audit ValidatorResult = %s, want unverified", row.ValidatorResult)
	}
	if row.UsageSource != "estimate" {
		t.Fatalf("audit UsageSource = %s, want estimate", row.UsageSource)
	}
	if !strings.Contains(row.FailoverChainJSON, "fail_empty_content") {
		t.Fatalf("failover chain must carry fail_empty_content: %s", row.FailoverChainJSON)
	}
	// Metric-only failure: the empty route records the code but takes
	// no circuit penalty.
	mt, _ := mem.GetMetrics("r1")
	if mt.LastErrorCode != "fail_empty_content" {
		t.Fatalf("r1 LastErrorCode = %s, want fail_empty_content", mt.LastErrorCode)
	}
	if mt.FailureCount != 1 {
		t.Fatalf("r1 FailureCount = %d, want 1", mt.FailureCount)
	}
}

// Every candidate empty -> 502, no audit row (failures never audit).
func TestChatStreamingEmptyOnly(t *testing.T) {
	empty := sseUpstream(t, emptySSEBody)
	defer empty.Close()
	gw, mem := testGateway(t, empty.URL+"/v1")
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, streamChatRequest(`{"model":"coder","stream":true,"messages":[{"role":"user","content":"2+2?"}]}`))
	if rec.Code != 502 {
		t.Fatalf("chat status %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "all_candidates_failed") {
		t.Fatalf("502 body must carry all_candidates_failed: %s", rec.Body.String())
	}
	logs, _ := mem.ListAudit(10, 0)
	if len(logs) != 0 {
		t.Fatalf("empty-stream 502 must not write audit, got %d rows", len(logs))
	}
}

// A usage chunk inside the stream bills actuals and keeps the
// upstream source label.
func TestChatStreamingUsageChunk(t *testing.T) {
	up := sseUpstream(t, usageSSEBody)
	defer up.Close()
	gw, mem := testGateway(t, up.URL+"/v1")
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, streamChatRequest(`{"model":"coder","stream":true,"messages":[{"role":"user","content":"2+2?"}]}`))
	if rec.Code != 200 {
		t.Fatalf("chat status %d body %s", rec.Code, rec.Body.String())
	}
	logs, _ := mem.ListAudit(10, 0)
	if len(logs) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(logs))
	}
	row := logs[0]
	if row.BilledTokens != 15 {
		t.Fatalf("audit BilledTokens = %d, want 15 (usage chunk)", row.BilledTokens)
	}
	if row.UsageSource != "upstream" {
		t.Fatalf("audit UsageSource = %s, want upstream", row.UsageSource)
	}
	if row.ValidatorResult != "unverified" {
		t.Fatalf("audit ValidatorResult = %s, want unverified", row.ValidatorResult)
	}
}
