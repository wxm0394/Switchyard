package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
	"github.com/wxm0394/Switchyard/internal/crypto"
)

func testGateway(t *testing.T, upstreamURL string) (*Gateway, *store.MemStore) {
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
	_ = mem.UpsertKey(scheduler.ProviderKey{ID: "k1", Name: "Test", ProviderType: "openai-compatible", EncryptedKey: enc, KeyMask: "test••••key", KeyState: "healthy", BaseURL: upstreamURL})
	_ = mem.UpsertRoute(scheduler.Route{ID: "r1", Name: "Test route", ProviderKeyID: "k1", ModelID: "test-model", DisplayName: "test-model", BaseURL: upstreamURL, ProviderType: "openai-compatible", UpstreamModel: "test-model", TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsVision: true, SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: 128000, ReliabilityScore: 100, CostWeight: 1, Enabled: true})
	_ = mem.UpsertAlias(store.Alias{ID: "a1", Alias: "coder", TargetModels: []string{"test-model"}, Description: "coder"})
	_ = mem.UpsertToken(store.ClientToken{ID: "t1", Name: "test", Token: "sk-portal-test", CreatedAt: "2026-01-01T00:00:00Z", Active: true})
	gw := New(mem, Config{MasterKey: keyBytes, Decrypt: func(enc string) (string, error) { return crypto.Decrypt(keyBytes, enc) }})
	return gw, mem
}

func TestModelsAliasOnly(t *testing.T) {
	gw, _ := testGateway(t, "http://example.invalid/v1")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	gw.HandleModels(rec, req)
	if rec.Code != 200 {
		t.Fatalf("models status %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "coder" {
		t.Fatalf("models must list aliases only, got %+v", body.Data)
	}
}

func TestChatAuthAndSuccess(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "test-model" {
			t.Errorf("upstream model = %v, want test-model", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","choices":[{"message":{"content":"4"},"finish_reason":"stop"}],"usage":{"total_tokens":195}}`))
	}))
	defer up.Close()
	gw, mem := testGateway(t, up.URL+"/v1")
	// No token -> 401.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"coder","messages":[{"role":"user","content":"2+2?"}]}`))
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no token status %d, want 401", rec.Code)
	}
	// With token -> 200, billed from upstream usage.
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"coder","messages":[{"role":"user","content":"2+2?"}]}`))
	req.Header.Set("Authorization", "Bearer sk-portal-test")
	rec = httptest.NewRecorder()
	gw.HandleChat(rec, req)
	if rec.Code != 200 {
		t.Fatalf("chat status %d body %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"content":"4"`)) {
		t.Fatalf("chat body missing content: %s", rec.Body.String())
	}
	logs, _ := mem.ListAudit(10, 0)
	if len(logs) != 1 || logs[0].BilledTokens != 195 || logs[0].StatusCode != 200 {
		t.Fatalf("audit = %+v", logs)
	}
}

func TestChatNoHealthyRoute(t *testing.T) {
	gw, mem := testGateway(t, "http://example.invalid/v1")
	// Disable the only route.
	_ = mem.UpsertRoute(scheduler.Route{ID: "r1", Name: "Test route", ProviderKeyID: "k1", ModelID: "test-model", DisplayName: "test-model", BaseURL: "http://example.invalid/v1", ProviderType: "openai-compatible", UpstreamModel: "test-model", TaskRole: "auto", RouteState: "disabled", CircuitState: "closed", SupportsVision: true, SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: 128000, ReliabilityScore: 100, CostWeight: 1, Enabled: false})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"coder","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-portal-test")
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, req)
	if rec.Code != 503 {
		t.Fatalf("disabled route status %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no_healthy_route") {
		t.Fatalf("503 body must carry no_healthy_route: %s", rec.Body.String())
	}
	logs, _ := mem.ListAudit(10, 0)
	if len(logs) != 0 {
		t.Fatalf("503 no-healthy-route must not write audit, got %d rows", len(logs))
	}
}

func TestAdminGuard(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/overview", nil)
	req.Header.Set("X-Admin-Password", "wrong")
	ok, status, _ := DecideAdminAccess(req, AdminGuardConfig{Password: "correct", Production: true})
	if ok || status != 401 {
		t.Fatalf("wrong admin password: ok=%v status=%d", ok, status)
	}
	req.Header.Set("X-Admin-Password", "correct")
	ok, _, _ = DecideAdminAccess(req, AdminGuardConfig{Password: "correct", Production: true})
	if !ok {
		t.Fatal("correct admin password must pass")
	}
	// Production without a password and without opt-out -> 503, no flag.
	req2 := httptest.NewRequest(http.MethodGet, "/api/gateway/overview", nil)
	ok, status, body := DecideAdminAccess(req2, AdminGuardConfig{Password: "", Production: true, AllowNoAuth: false})
	if ok || status != 503 {
		t.Fatalf("no password in production: ok=%v status=%d", ok, status)
	}
	if _, has := body["requiresAdminPassword"]; has {
		t.Fatal("no-password 503 must not carry requiresAdminPassword")
	}
	// Host guard.
	req3 := httptest.NewRequest(http.MethodGet, "/api/gateway/overview", nil)
	req3.Host = "evil.example.com"
	req3.Header.Set("X-Admin-Password", "correct")
	ok, status, _ = DecideAdminAccess(req3, AdminGuardConfig{Password: "correct", AllowedHosts: []string{"gw.example.com"}})
	if ok || status != 403 {
		t.Fatalf("disallowed host: ok=%v status=%d", ok, status)
	}
}
