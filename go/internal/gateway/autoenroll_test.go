package gateway

import (
	"testing"

	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
)

func setupAutoEnroll(t *testing.T) *Gateway {
	t.Helper()
	st := store.NewMemStore()
	_ = st.UpsertKey(scheduler.ProviderKey{ID: "k1", Name: "kaggle", KeyState: "healthy", EncryptedKey: "enc:x"})
	_ = st.UpsertAlias(store.Alias{ID: "a1", Alias: "coder", TargetModels: []string{"model-a"}})
	_ = st.UpsertAlias(store.Alias{ID: "a2", Alias: "fast", TargetModels: []string{"model-a"}})
	_ = st.UpsertAlias(store.Alias{ID: "a3", Alias: "vision", TargetModels: []string{"model-a"}})
	_ = st.UpsertRoute(scheduler.Route{ID: "r1", ModelID: "model-new", ProviderKeyID: "k1", Enabled: true, RouteState: "healthy", SupportsVision: false})
	_ = st.UpsertRoute(scheduler.Route{ID: "r2", ModelID: "model-vis", ProviderKeyID: "k1", Enabled: true, RouteState: "healthy", SupportsVision: true})
	return New(st, Config{})
}

func aliasTargets(g *Gateway, name string) []string {
	a, _ := g.Store.GetAlias(name)
	return a.TargetModels
}

func TestAutoEnrollRouteAppendsLastSkipsVision(t *testing.T) {
	g := setupAutoEnroll(t)
	route := scheduler.Route{ID: "r1", ModelID: "model-new", SupportsVision: false}
	enrolled := g.AutoEnrollRoute(route)
	if len(enrolled) != 2 {
		t.Fatalf("expected enrolment into coder+fast only, got %v", enrolled)
	}
	if got := aliasTargets(g, "coder"); len(got) != 2 || got[1] != "model-new" {
		t.Fatalf("coder targets wrong: %v", got)
	}
	if got := aliasTargets(g, "vision"); len(got) != 1 {
		t.Fatalf("vision must be skipped for non-vision route: %v", got)
	}
	// Second call: alias already contains the modelId, so it is skipped.
	if enrolled := g.AutoEnrollRoute(route); len(enrolled) != 0 {
		t.Fatalf("re-enrolment must skip already-containing aliases, got %v", enrolled)
	}
}

func TestAutoEnrollRouteVisionRoute(t *testing.T) {
	g := setupAutoEnroll(t)
	route := scheduler.Route{ID: "r2", ModelID: "model-vis", SupportsVision: true}
	if enrolled := g.AutoEnrollRoute(route); len(enrolled) != 3 {
		t.Fatalf("vision-capable route must enrol into all three aliases, got %v", enrolled)
	}
}

func TestAutoEnrollVerifiedRoutesStartup(t *testing.T) {
	g := setupAutoEnroll(t)
	// r1 becomes verified by a recorded success; r2 stays untested
	// (no successes) and must NOT enrol at startup.
	_ = g.Store.RecordSuccess("r1", "k1", 100, 0, 50)
	enrolled := g.AutoEnrollVerifiedRoutes()
	if len(enrolled) != 2 {
		t.Fatalf("startup scan must enrol verified r1 into coder+fast only, got %v", enrolled)
	}
	if got := aliasTargets(g, "coder"); len(got) != 2 || got[1] != "model-new" {
		t.Fatalf("coder targets wrong after startup scan: %v", got)
	}
	if got := aliasTargets(g, "vision"); len(got) != 1 {
		t.Fatalf("untested vision route must not enrol at startup: %v", got)
	}
}
