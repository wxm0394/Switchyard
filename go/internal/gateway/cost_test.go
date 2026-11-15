package gateway

import (
	"database/sql"
	"math"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCostForCalculation(t *testing.T) {
	// Scenario 1 (Spec 5.1):
	// z-ai/glm-5.3-flash-free uses GLM-5.3-Flash paid rate:
	// input 1,000,000 * $0.15/1M + output 500,000 * $0.50/1M = $0.40 (±0.001)
	cost, ok := CostFor("z-ai/glm-5.3-flash-free", 1_000_000, 500_000)
	if !ok {
		t.Fatalf("expected ok=true for z-ai/glm-5.3-flash-free")
	}
	if math.Abs(cost-0.40) > 0.001 {
		t.Fatalf("expected cost ~0.40, got %.6f", cost)
	}
}

func TestCostForUnknownModel(t *testing.T) {
	// Scenario 2 (Spec 5.1):
	// Unknown model returns ok=false without panic
	cost, ok := CostFor("non-existent/unknown-model-99b", 1_000_000, 500_000)
	if ok {
		t.Fatalf("expected ok=false for unknown model, got ok=true with cost=%v", cost)
	}
	if cost != 0 {
		t.Fatalf("expected cost=0 for unknown model, got %v", cost)
	}
}

func TestCostSummaryExcludesUnpricedRows(t *testing.T) {
	// Rule (user order 2026-11-15): the cost view only counts PRICED
	// requests. Rows with cost_usd NULL — no price, or no usage split —
	// are excluded from every aggregate, so everything shown is priced.
	// In-memory SQLite with >= 2 models, >= 3 CST days, and NULL cost_usd rows
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open memory sqlite: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE audit_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL,
			selected_model_id TEXT,
			requested_model TEXT,
			billed_tokens INTEGER,
			estimated_tokens INTEGER,
			usage_source TEXT,
			preview_prompt TEXT,
			session_key TEXT,
			client_token_id TEXT,
			client_token_name TEXT,
			cost_usd REAL
		);
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	rows := []struct {
		ts     string
		model  string
		tokens int64
		client string
		cost   *float64
	}{
		{"2026-11-11T10:00:00Z", "ZhipuAI/GLM-5.2", 1500000, "全客户端通用访问令牌", ptrFloat(3.60)},
		{"2026-11-11T16:30:00Z", "nvidia/nemotron-3.5-lightning-30b-a3b", 3000000, "全客户端通用访问令牌", ptrFloat(0.34)},
		{"2026-11-13T08:00:00Z", "ZhipuAI/GLM-5.2", 1000000, "RooCode 研发令牌", ptrFloat(2.90)},
		{"2026-11-13T09:00:00Z", "custom/unpriced-model", 5000000, "全客户端通用访问令牌", nil},
	}

	for _, r := range rows {
		_, err := db.Exec(
			`INSERT INTO audit_history (timestamp, selected_model_id, requested_model, billed_tokens, client_token_name, cost_usd) VALUES (?, ?, ?, ?, ?, ?)`,
			r.ts, r.model, r.model, r.tokens, r.client, r.cost,
		)
		if err != nil {
			t.Fatalf("insert row: %v", err)
		}
	}

	nowUTC := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	summary, err := QueryCostSummary(db, "audit_history", "7d", nowUTC)
	if err != nil {
		t.Fatalf("QueryCostSummary failed: %v", err)
	}

	if math.Abs(summary.TotalCostUSD-6.84) > 0.001 {
		t.Fatalf("expected TotalCostUSD=6.84, got %.2f", summary.TotalCostUSD)
	}
	if summary.UnknownPriceTokens != 0 {
		t.Fatalf("expected UnknownPriceTokens=0 (NULL rows excluded), got %d", summary.UnknownPriceTokens)
	}
	if summary.TotalTokens != 5500000 {
		t.Fatalf("expected TotalTokens=5500000 (priced rows only), got %d", summary.TotalTokens)
	}
	if summary.TotalRequests != 3 {
		t.Fatalf("expected TotalRequests=3 (priced rows only), got %d", summary.TotalRequests)
	}
	if len(summary.ByModel) != 2 {
		t.Fatalf("expected 2 priced models, got %d: %+v", len(summary.ByModel), summary.ByModel)
	}
	for _, m := range summary.ByModel {
		if m.ModelID == "custom/unpriced-model" {
			t.Fatalf("unpriced model must not appear in ByModel: %+v", summary.ByModel)
		}
		if m.PriceUnknown || m.CostUSD == nil {
			t.Fatalf("every ByModel row must be priced: %+v", m)
		}
	}
	if len(summary.ByDay) != 3 {
		t.Fatalf("expected 3 CST days, got %d: %+v", len(summary.ByDay), summary.ByDay)
	}
	if summary.ByDay[0].Date != "2026-11-11" || summary.ByDay[1].Date != "2026-11-12" || summary.ByDay[2].Date != "2026-11-13" {
		t.Fatalf("unexpected CST day dates: %+v", summary.ByDay)
	}
}

func ptrFloat(v float64) *float64 {
	return &v
}
