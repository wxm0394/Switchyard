package gateway

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func makeCostTestDB(t *testing.T, withCostCol bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	defer db.Close()
	cols := "request_id TEXT, timestamp TEXT, selected_model_id TEXT, requested_model TEXT, client_token_name TEXT, billed_tokens INTEGER"
	if withCostCol {
		cols += ", cost_usd REAL"
	}
	if _, err := db.Exec("CREATE TABLE audit_history (" + cols + ")"); err != nil {
		t.Fatalf("create audit_history: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if withCostCol {
		_, err = db.Exec(`INSERT INTO audit_history (request_id, timestamp, selected_model_id, requested_model, client_token_name, billed_tokens, cost_usd) VALUES
			('r1', ?, 'z-ai/glm-5.3-flash-free', 'z-ai/glm-5.3-flash-free', 'test-client', 1500000, 0.40),
			('r2', ?, 'z-ai/glm-5.3-flash-free', 'z-ai/glm-5.3-flash-free', 'test-client', 500000, 0.10),
			('r3', ?, 'unknown-model-xyz', 'unknown-model-xyz', 'test-client', 100000, NULL)`, now, now, now)
	} else {
		_, err = db.Exec(`INSERT INTO audit_history (request_id, timestamp, selected_model_id, requested_model, client_token_name, billed_tokens) VALUES
			('r1', ?, 'z-ai/glm-5.3-flash-free', 'z-ai/glm-5.3-flash-free', 'test-client', 1500000)`, now)
	}
	if err != nil {
		t.Fatalf("insert rows: %v", err)
	}
	return path
}

func TestHandleCostSummaryOK(t *testing.T) {
	path := makeCostTestDB(t, true)
	old := costHistoryDBPath
	costHistoryDBPath = path
	defer func() { costHistoryDBPath = old }()

	req := httptest.NewRequest(http.MethodGet, "/api/gateway/cost/summary?range=7d", nil)
	w := httptest.NewRecorder()
	(&Gateway{}).handleCostSummary(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp CostSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Range != "7d" {
		t.Fatalf("range: got %q", resp.Range)
	}
	if resp.TotalCostUSD != 0.50 {
		t.Fatalf("total cost: got %v, want 0.50", resp.TotalCostUSD)
	}
	if resp.TotalTokens != 2000000 {
		t.Fatalf("total tokens: got %d, want 2000000 (priced rows only)", resp.TotalTokens)
	}
	if resp.TotalRequests != 2 {
		t.Fatalf("total requests: got %d, want 2 (priced rows only)", resp.TotalRequests)
	}
	if resp.UnknownPriceTokens != 0 {
		t.Fatalf("unknown price tokens: got %d, want 0 (NULL rows excluded)", resp.UnknownPriceTokens)
	}
	if resp.Note != "参考价，按市价估算，非实际扣费" {
		t.Fatalf("note: got %q", resp.Note)
	}
}

func TestHandleCostSummaryMissingColumn503(t *testing.T) {
	path := makeCostTestDB(t, false)
	old := costHistoryDBPath
	costHistoryDBPath = path
	defer func() { costHistoryDBPath = old }()

	req := httptest.NewRequest(http.MethodGet, "/api/gateway/cost/summary?range=7d", nil)
	w := httptest.NewRecorder()
	(&Gateway{}).handleCostSummary(w, req)
	if w.Code != 503 {
		t.Fatalf("expected 503 for missing cost_usd column, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cost_usd") {
		t.Fatalf("503 body should name the missing column, got: %s", w.Body.String())
	}
}

func TestHandleCostSummaryMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/cost/summary", nil)
	w := httptest.NewRecorder()
	(&Gateway{}).handleCostSummary(w, req)
	if w.Code != 405 {
		t.Fatalf("expected 405 for POST, got %d", w.Code)
	}
}

func TestParseUsageSplit(t *testing.T) {
	p, c, ok := parseUsageSplit([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`), false)
	if !ok || p != 100 || c != 50 {
		t.Fatalf("JSON split: got (%d,%d,%v)", p, c, ok)
	}
	if _, _, ok := parseUsageSplit([]byte(`{"choices":[]}`), false); ok {
		t.Fatalf("JSON without usage must be ok=false")
	}
	if _, _, ok := parseUsageSplit([]byte(`{"usage":{"total_tokens":150}}`), false); ok {
		t.Fatalf("JSON with total only must be ok=false (no split to price)")
	}
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n"
	p, c, ok = parseUsageSplit([]byte(sse), true)
	if !ok || p != 10 || c != 5 {
		t.Fatalf("SSE split: got (%d,%d,%v)", p, c, ok)
	}
	if _, _, ok := parseUsageSplit([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n"), true); ok {
		t.Fatalf("SSE without usage must be ok=false")
	}
}
