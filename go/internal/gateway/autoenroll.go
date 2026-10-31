package gateway

import (
	"log"

	"github.com/wxm0394/Switchyard/internal/scheduler"
)

// Auto-enroll, ported from Node scheduler.ts.
//
// Node has TWO triggers, not one; both are ported here, because
// porting only one would silently change behaviour:
//
//  1. On success (handleRouteSuccess step 6): a route that just
//     succeeded is proven working, so it enters Layer 1 aliases
//     (last position) without manual editing. This path does NOT
//     re-check verification status: success itself is the proof.
//  2. At startup (server.ts listen callback): autoEnrollVerifiedRoutes
//     scans all enabled routes and enrols those whose verification
//     status is "verified" (successCount > 0, no failure flags, no
//     disabled/exhausted/unavailable_for_key/cooldown state).
//
// Shared rules, identical in both triggers:
//  - Every alias except "vision" is a candidate; "vision" only when
//    the route supports vision (capability-filtered).
//  - An alias that already contains the route's modelId is SKIPPED.
//    This is why reordering an in-pool route sticks, but a removed
//    route re-enrols on its next success.
//  - The modelId is appended at the LAST position, never inserted
//    in front.
//  - Any error is logged and swallowed (fail-open, as in Node); a
//    failed enrolment must never break a chat response.
//
// In Go this lives in the gateway package, not scheduler: scheduler
// cannot import store (store already imports scheduler), and
// enrolment needs alias persistence.

func containsModel(targets []string, modelID string) bool {
	for _, t := range targets {
		if t == modelID {
			return true
		}
	}
	return false
}

func (g *Gateway) enrollRouteIntoAliases(route scheduler.Route, logSuffix string) []string {
	aliases, err := g.Store.ListAliases()
	if err != nil {
		log.Printf("[AUTO-ENROLL] failed: %v", err)
		return nil
	}
	enrolled := []string{}
	for _, alias := range aliases {
		if alias.Alias == "vision" && !route.SupportsVision {
			continue
		}
		if containsModel(alias.TargetModels, route.ModelID) {
			continue
		}
		next := append(append([]string{}, alias.TargetModels...), route.ModelID)
		alias.TargetModels = next
		if err := g.Store.UpsertAlias(alias); err != nil {
			log.Printf("[AUTO-ENROLL] failed: %v", err)
			continue
		}
		log.Printf("[AUTO-ENROLL] route %s -> alias %s (%s)", route.ModelID, alias.Alias, logSuffix)
		enrolled = append(enrolled, alias.Alias)
	}
	return enrolled
}

// AutoEnrollRoute is the on-success trigger: call it after a confirmed
// HTTP 200 success (validator-passed in the non-streaming path, or a
// completed stream), never on validator failure, upstream failure, or
// an unconfirmed pass-through. It returns the alias names the route
// was appended to.
func (g *Gateway) AutoEnrollRoute(route scheduler.Route) []string {
	return g.enrollRouteIntoAliases(route, "last position, on success")
}

// EnrolledPair is one route/alias pair from the startup scan.
type EnrolledPair struct {
	RouteID string
	ModelID string
	Alias   string
}

// AutoEnrollVerifiedRoutes is the startup trigger: it enrols every
// enabled, verification-"verified" route into every eligible alias.
// Call it once at server startup, as Node does in its listen callback.
// It returns the pairs it enrolled.
func (g *Gateway) AutoEnrollVerifiedRoutes() []EnrolledPair {
	routes, keys, _, _, err := g.loadFleet()
	if err != nil {
		log.Printf("[AUTO-ENROLL] failed: %v", err)
		return nil
	}
	enrolled := []EnrolledPair{}
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		k, ok := keys[route.ProviderKeyID]
		var kp *scheduler.ProviderKey
		if ok {
			kp = &k
		}
		metrics, _ := g.Store.GetMetrics(route.ID)
		eff := g.effectiveStateWithHeal(route, kp)
		if verificationStatus(route, kp, metrics, eff) != "verified" {
			continue
		}
		// Re-read aliases per route, because an earlier route in this
		// scan may have appended to them.
		for _, aliasName := range g.enrollRouteIntoAliases(route, "last position") {
			enrolled = append(enrolled, EnrolledPair{RouteID: route.ID, ModelID: route.ModelID, Alias: aliasName})
		}
	}
	return enrolled
}
