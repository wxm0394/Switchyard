package store

import (
	"path/filepath"
	"testing"

	"github.com/wxm0394/Switchyard/internal/scheduler"
)

// Phase 6: UpsertRoute used to ship 27 placeholders for 28 columns,
// so creating any route through the SQLite store failed with
// "SQL logic error: 27 values for 28 columns". The binary battery
// caught it because gateway tests only use MemStore.
func TestSQLiteUpsertRouteRoundTrip(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := scheduler.Route{ID: "r1", Name: "m", ProviderKeyID: "k1", ModelID: "m", DisplayName: "m", BaseURL: "https://example.invalid/v1", ProviderType: "openai-compatible", UpstreamModel: "m", TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: 128000, ReliabilityScore: 100, Enabled: true}
	if err := st.UpsertRoute(r); err != nil {
		t.Fatalf("UpsertRoute: %v", err)
	}
	routes, err := st.ListRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].ModelID != "m" || routes[0].BaseURL != "https://example.invalid/v1" {
		t.Fatalf("ListRoutes = %+v", routes)
	}
}

// Phase 6: audit rows sharing one timestamp second must still come
// back newest-inserted first (rowid tiebreak), and the 200-row trim
// must keep the newest rows under the same tie.
func TestSQLiteAuditSameTimestampOrder(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ts := "2026-10-31T12:00:00Z"
	for _, id := range []string{"req-old", "req-new"} {
		if err := st.AddAudit(AuditRow{RequestID: id, Timestamp: ts, SessionKey: "s", ClientTokenID: "t", ClientTokenName: "t", DetectedTaskType: "fast", SelectedProviderKeyID: "k", SelectedRouteID: "r", SelectedModelID: "m", StatusCode: 200, ValidatorResult: "pass"}); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := st.ListAudit(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].RequestID != "req-new" || logs[1].RequestID != "req-old" {
		t.Fatalf("same-timestamp audit order = %v, want [req-new req-old]", logs)
	}
}
