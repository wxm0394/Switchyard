package gateway

import (
	"testing"

	"github.com/wxm0394/Switchyard/internal/scheduler"
)

// Phase 7 reproduction: admin route creation used intVal for costWeight,
// so a JSON 0.5 was truncated to int(0.5) = 0. A 0.5 route must survive
// creation unchanged.
func TestRouteFromBodyCostWeightFloat(t *testing.T) {
	body := map[string]interface{}{
		"modelId": "qwen3.6-27b", "costWeight": 0.5,
	}
	route := routeFromBody(body, "k1", "https://example.invalid/v1")
	if route.CostWeight != 0.5 {
		t.Fatalf("routeFromBody costWeight 0.5 = %v, want 0.5 (must not truncate to 0)", route.CostWeight)
	}
	body["costWeight"] = float64(3)
	route = routeFromBody(body, "k1", "https://example.invalid/v1")
	if route.CostWeight != 3 {
		t.Fatalf("routeFromBody costWeight 3 = %v, want 3", route.CostWeight)
	}
}

// Cost formula (gateway auto scoring): cCost = clamp01((w-1)/9).
// 0.5 must map to the cheapest tier, exactly like integer 1, and
// integers 1..10 must behave as before: score(0.5) == score(1) >
// score(5) > score(10), with all other scoring inputs equal.
// This drives the real selectCandidates path, not a copy of the formula.
func TestSelectCandidatesCostWeight(t *testing.T) {
	gw, mem := testGateway(t, "http://example.invalid/v1")
	cases := []struct {
		id     string
		model  string
		weight float64
	}{
		{"r-cost-05", "cost-05", 0.5},
		{"r-cost-1", "cost-1", 1},
		{"r-cost-5", "cost-5", 5},
		{"r-cost-10", "cost-10", 10},
	}
	for _, c := range cases {
		if err := mem.UpsertRoute(scheduler.Route{ID: c.id, Name: c.model, ProviderKeyID: "k1", ModelID: c.model, DisplayName: c.model, BaseURL: "http://example.invalid/v1", ProviderType: "openai-compatible", UpstreamModel: c.model, TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: 128000, ReliabilityScore: 100, CostWeight: c.weight, Enabled: true}); err != nil {
			t.Fatalf("UpsertRoute %s: %v", c.id, err)
		}
	}
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
	cls := scheduler.Classification{TaskType: "fast", RoutingMode: "auto", EstimatedTokens: 100}
	cands := gw.selectCandidates(cls, "", nil, routes, keys, scheduler.SchedulerConfig{})
	scoreByModel := map[string]float64{}
	for _, c := range cands {
		scoreByModel[c.Route.ModelID] = c.Score
	}
	for _, m := range []string{"cost-05", "cost-1", "cost-5", "cost-10"} {
		if _, ok := scoreByModel[m]; !ok {
			t.Fatalf("candidate %s missing from selectCandidates, got %v", m, scoreByModel)
		}
	}
	if scoreByModel["cost-05"] != scoreByModel["cost-1"] {
		t.Fatalf("score(0.5)=%v, score(1)=%v, want equal (both cheapest)", scoreByModel["cost-05"], scoreByModel["cost-1"])
	}
	if !(scoreByModel["cost-1"] > scoreByModel["cost-5"] && scoreByModel["cost-5"] > scoreByModel["cost-10"]) {
		t.Fatalf("want score(1) > score(5) > score(10), got %v", scoreByModel)
	}
}
