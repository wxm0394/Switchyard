package store

import (
	"path/filepath"
	"testing"

	"github.com/wxm0394/Switchyard/internal/scheduler"
)

// Phase 7 reproduction: Node production DB stores ONE route with REAL
// cost_weight 0.5 (qwen3.6-27b, Qwen GPU). Go main ed88342a scanned
// cost_weight into an int, so ListRoutes failed with:
//
//	sql: Scan error on column index 27, name "cost_weight":
//	converting driver.Value type float64 ("0.5") to a int
//
// and /v1/models returned 500. This test inserts the row the way Node
// does (a REAL 0.5 in the column, via a direct UPDATE after upsert)
// and requires ListRoutes to load it as 0.5 instead of failing.
func TestSQLiteLoadRealCostWeight(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := scheduler.Route{ID: "r-qwen", Name: "qwen", ProviderKeyID: "k1", ModelID: "qwen3.6-27b", DisplayName: "qwen3.6-27b", BaseURL: "https://example.invalid/v1", ProviderType: "openai-compatible", UpstreamModel: "qwen3.6-27b", TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: 131072, ReliabilityScore: 100, CostWeight: 1, Enabled: true}
	if err := st.UpsertRoute(r); err != nil {
		t.Fatalf("UpsertRoute: %v", err)
	}
	// Mimic the Node production row: REAL 0.5, not an integer.
	if _, err := st.db.Exec(`UPDATE model_routes SET cost_weight = 0.5 WHERE id = ?`, "r-qwen"); err != nil {
		t.Fatalf("UPDATE cost_weight: %v", err)
	}
	routes, err := st.ListRoutes()
	if err != nil {
		t.Fatalf("ListRoutes with REAL 0.5 cost_weight must not fail, got: %v", err)
	}
	if len(routes) != 1 || routes[0].CostWeight != 0.5 {
		t.Fatalf("ListRoutes = %+v, want exactly one route with CostWeight 0.5", routes)
	}
}

// A fresh Go DB stores integer cost_weights; those must still load
// unchanged after the type change (SQLite integer 1 -> float64 1).
func TestSQLiteLoadIntegerCostWeightUnchanged(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := scheduler.Route{ID: "r1", Name: "m", ProviderKeyID: "k1", ModelID: "m", DisplayName: "m", BaseURL: "https://example.invalid/v1", ProviderType: "openai-compatible", UpstreamModel: "m", TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: 128000, ReliabilityScore: 100, CostWeight: 3, Enabled: true}
	if err := st.UpsertRoute(r); err != nil {
		t.Fatalf("UpsertRoute: %v", err)
	}
	routes, err := st.ListRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].CostWeight != 3 {
		t.Fatalf("ListRoutes = %+v, want CostWeight 3", routes)
	}
}
