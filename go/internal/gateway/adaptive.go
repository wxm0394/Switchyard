package gateway

import (
	"time"

	"github.com/wxm0394/Multi-Armed-Bandit/bridge"
	"github.com/wxm0394/Switchyard/internal/scheduler"
)

// SetAdaptive enables adaptive rotation with c, or disables it with nil.
// Call it once at startup, before serving requests: the field and the
// rotator hook are read without locking afterwards.
func (g *Gateway) SetAdaptive(c *bridge.Controller) {
	g.Adaptive = c
	if c == nil {
		g.Rotator.WeightFn = nil
		return
	}
	g.Rotator.WeightFn = g.adaptiveWeights
}

// adaptiveWeights is the Rotator.WeightFn hook. It returns nil (use the
// static weights) when the controller is off or in shadow mode.
func (g *Gateway) adaptiveWeights(poolKey string, size scheduler.SizeClass, pool []scheduler.Candidate, static []float64) []float64 {
	if g.Adaptive == nil {
		return nil
	}
	routes := make([]bridge.Route, len(pool))
	for i, c := range pool {
		routes[i] = bridge.Route{ID: c.Route.ID, Private: c.Route.PrivateOnly}
	}
	return g.Adaptive.Weights(poolKey, string(size), routes, static)
}

// observe reports the outcome of one upstream attempt. attempt must be the
// duration of that single attempt, not of the whole request.
func (g *Gateway) observe(cand scheduler.Candidate, size scheduler.SizeClass, category string, attempt time.Duration, stream bool) {
	if g.Adaptive == nil {
		return
	}
	g.Adaptive.Observe(cand.Route.ID, string(size), cand.Route.PrivateOnly, category, attempt, stream)
}
