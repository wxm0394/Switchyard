package gateway

import (
	"strings"
	"testing"

	"github.com/wxm0394/Switchyard/internal/scheduler"
)

// Session affinity for alias mode: a stored affinity pointing at a
// route in the alias pool must pin that route first (alias branches
// previously ignored affinity entirely), while a stale affinity or
// one that fails HardFilter must not disturb normal ordering.
func TestSelectCandidatesAliasAffinity(t *testing.T) {
	gw, mem := testGateway(t, "http://example.invalid/v1")
	add := func(id, model string, maxCtx int) {
		if err := mem.UpsertRoute(scheduler.Route{ID: id, Name: model, ProviderKeyID: "k1", ModelID: model, DisplayName: model, BaseURL: "http://example.invalid/v1", ProviderType: "openai-compatible", UpstreamModel: model, TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: maxCtx, ReliabilityScore: 100, CostWeight: 1, Enabled: true}); err != nil {
			t.Fatalf("UpsertRoute %s: %v", id, err)
		}
	}
	add("r-a", "model-a", 128000)
	add("r-b", "model-b", 128000)
	add("r-c", "model-c", 2000) // holds a 100-token estimate, too small for 5000
	add("r-other", "model-other", 128000)
	routes, err := mem.ListRoutes()
	if err != nil {
		t.Fatal(err)
	}
	memKeys, err := mem.ListKeys()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]scheduler.ProviderKey{}
	for _, k := range memKeys {
		keys[k.ID] = k
	}
	targets := []string{"model-a", "model-b", "model-c"}
	cls := scheduler.Classification{TaskType: "fast", RoutingMode: "pool", EstimatedTokens: 100, IsExplicitSession: true, SessionKey: "sess-1"}

	// No affinity: alias order rules, model-a (index 0) first.
	cands, pinned := gw.selectCandidates(cls, "", targets, routes, keys, scheduler.SchedulerConfig{})
	if pinned {
		t.Fatal("no affinity stored, pinned must be false")
	}
	if len(cands) != 3 || cands[0].Route.ID != "r-a" {
		t.Fatalf("no affinity: first = %v, want r-a", routeIDs(cands))
	}

	// Affinity to r-b (index 1): pinned first, marked sticky.
	if err := mem.SetAffinity("sess-1", "r-b", "k1", 1800); err != nil {
		t.Fatal(err)
	}
	cands, pinned = gw.selectCandidates(cls, "", targets, routes, keys, scheduler.SchedulerConfig{})
	if !pinned {
		t.Fatal("affinity to r-b must pin")
	}
	if len(cands) != 3 || cands[0].Route.ID != "r-b" || !cands[0].IsSticky {
		t.Fatalf("affinity to r-b: first = %v sticky=%v, want r-b sticky", routeIDs(cands), cands[0].IsSticky)
	}
	if cands[1].Route.ID != "r-a" || cands[2].Route.ID != "r-c" {
		t.Fatalf("pin must preserve the rest of the order, got %v", routeIDs(cands))
	}

	// Affinity to a route outside this alias pool: ordering unchanged.
	if err := mem.SetAffinity("sess-1", "r-other", "k1", 1800); err != nil {
		t.Fatal(err)
	}
	cands, pinned = gw.selectCandidates(cls, "", targets, routes, keys, scheduler.SchedulerConfig{})
	if pinned {
		t.Fatal("affinity outside the alias pool must not pin")
	}
	if len(cands) != 3 || cands[0].Route.ID != "r-a" {
		t.Fatalf("outside-pool affinity: first = %v, want r-a", routeIDs(cands))
	}

	// Affinity to r-c, whose 2000 context cannot hold a 5000-token
	// estimate: HardFilter drops it, so it is never forced first.
	if err := mem.SetAffinity("sess-1", "r-c", "k1", 1800); err != nil {
		t.Fatal(err)
	}
	big := scheduler.Classification{TaskType: "reasoning", RoutingMode: "pool", EstimatedTokens: 5000, IsExplicitSession: true, SessionKey: "sess-1"}
	cands, pinned = gw.selectCandidates(big, "", targets, routes, keys, scheduler.SchedulerConfig{})
	if pinned {
		t.Fatal("HardFilter-failing affinity must not pin")
	}
	for _, c := range cands {
		if c.Route.ID == "r-c" {
			t.Fatalf("r-c must be HardFilter-dropped for a 5000-token estimate, got %v", routeIDs(cands))
		}
	}
	if len(cands) != 2 || cands[0].Route.ID != "r-a" {
		t.Fatalf("HardFilter case: want [r-a r-b], got %v", routeIDs(cands))
	}
}

func routeIDs(cands []scheduler.Candidate) []string {
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.Route.ID)
	}
	return ids
}

// End to end: a client that sends no session header (Roo) gets a
// derived session key, success writes affinity under it, and the
// next rounds of the same conversation stick to the same model even
// though rotation would otherwise move the first pick (per-route
// currents persist across requests). A different conversation
// (different first user message) derives a different key and still
// rotates normally.
func TestDerivedSessionSticksAcrossRounds(t *testing.T) {
	s := newPlanActGateway(t)
	round1 := `{"model":"coder","messages":[{"role":"user","content":"session probe one"}]}`
	rec := s.doChat(t, "coder", round1, nil)
	if rec.Code != 200 {
		t.Fatalf("round 1 status %d body %s", rec.Code, rec.Body.String())
	}
	first := s.lastCaptured(t)

	// Same conversation, history resent in full like Roo does.
	round2 := `{"model":"coder","messages":[{"role":"user","content":"session probe one"},{"role":"assistant","content":"ok"},{"role":"user","content":"continue the work"}]}`
	rec = s.doChat(t, "coder", round2, nil)
	if rec.Code != 200 {
		t.Fatalf("round 2 status %d body %s", rec.Code, rec.Body.String())
	}
	if got := s.lastCaptured(t); got != first {
		t.Fatalf("round 2 served by %s, want sticky %s (rotation must be skipped for affinity)", got, first)
	}
	round3 := `{"model":"coder","messages":[{"role":"user","content":"session probe one"},{"role":"assistant","content":"ok"},{"role":"user","content":"continue the work"},{"role":"assistant","content":"done"},{"role":"user","content":"and the next step"}]}`
	rec = s.doChat(t, "coder", round3, nil)
	if rec.Code != 200 {
		t.Fatalf("round 3 status %d body %s", rec.Code, rec.Body.String())
	}
	if got := s.lastCaptured(t); got != first {
		t.Fatalf("round 3 served by %s, want sticky %s", got, first)
	}

	// Affinity is stored under the derived key (from the audit log),
	// pointing at the route that served round 1.
	logs, _ := s.mem.ListAudit(10, 0)
	var derivedKey string
	for _, l := range logs {
		if strings.HasPrefix(l.SessionKey, "derived_") {
			derivedKey = l.SessionKey
			break
		}
	}
	if derivedKey == "" {
		t.Fatalf("no audit row carries a derived session key, got %+v", logs)
	}
	if !strings.HasPrefix(derivedKey, "derived_t1_") {
		t.Fatalf("derived key = %q, want prefix derived_t1_", derivedKey)
	}
	routeID, _, ok, err := s.mem.GetAffinity(derivedKey)
	if err != nil || !ok {
		t.Fatalf("GetAffinity(%q) ok=%v err=%v", derivedKey, ok, err)
	}
	routes, err := s.mem.ListRoutes()
	if err != nil {
		t.Fatal(err)
	}
	served := ""
	for _, r := range routes {
		if r.ID == routeID {
			served = r.ModelID
		}
	}
	if served != first {
		t.Fatalf("affinity points at route %s (%s), want the round-1 model %s", routeID, served, first)
	}

	// A different conversation derives a different key and still
	// rotates: after round 1 the sticky route's rotation current was
	// debited, so the next rotation winner is the second alias entry.
	other := `{"model":"coder","messages":[{"role":"user","content":"a completely different task"}]}`
	rec = s.doChat(t, "coder", other, nil)
	if rec.Code != 200 {
		t.Fatalf("other conversation status %d body %s", rec.Code, rec.Body.String())
	}
	if got := s.lastCaptured(t); got == first {
		t.Fatalf("other conversation served by %s, want rotation to move it (got the sticky model)", got)
	}
	if got := s.lastCaptured(t); got != "qwen3.6-27b" {
		t.Fatalf("other conversation first pick = %s, want qwen3.6-27b (rotation winner)", got)
	}
}
