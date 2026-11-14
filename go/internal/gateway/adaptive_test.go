package gateway

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	bandit "github.com/wxm0394/Multi-Armed-Bandit"
	"github.com/wxm0394/Multi-Armed-Bandit/bridge"
	"github.com/wxm0394/Switchyard/internal/crypto"
	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
)

const okBody = `{"id":"x","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"total_tokens":10}}`

func okHandler(delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody))
	}
}

func statusHandler(delay time.Duration, code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func sseHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}
}

type fakeRoute struct {
	id      string
	handler http.HandlerFunc
	private bool
}

// adaptiveGateway builds a gateway whose alias "coder" lists the routes in
// the given order, each backed by its own fake upstream.
func adaptiveGateway(t *testing.T, ctl *bridge.Controller, routes ...fakeRoute) (*Gateway, *store.MemStore) {
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
	var targets []string
	for i, fr := range routes {
		srv := httptest.NewServer(fr.handler)
		t.Cleanup(srv.Close)
		kid, model := fmt.Sprintf("k%d", i+1), "model-"+fr.id
		_ = mem.UpsertKey(scheduler.ProviderKey{ID: kid, Name: kid, ProviderType: "openai-compatible", EncryptedKey: enc, KeyMask: "x", KeyState: "healthy", BaseURL: srv.URL + "/v1"})
		_ = mem.UpsertRoute(scheduler.Route{ID: fr.id, Name: fr.id, ProviderKeyID: kid, ModelID: model, DisplayName: model, BaseURL: srv.URL + "/v1", ProviderType: "openai-compatible", UpstreamModel: model, TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsVision: true, SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: 128000, ReliabilityScore: 100, CostWeight: 1, Enabled: true, PrivateOnly: fr.private, AllowSensitive: fr.private})
		targets = append(targets, model)
	}
	_ = mem.UpsertAlias(store.Alias{ID: "a1", Alias: "coder", TargetModels: targets, Description: "coder"})
	_ = mem.UpsertToken(store.ClientToken{ID: "t1", Name: "test", Token: "sk-portal-test", CreatedAt: "2026-01-01T00:00:00Z", Active: true})
	gw := New(mem, Config{MasterKey: keyBytes, Decrypt: func(enc string) (string, error) { return crypto.Decrypt(keyBytes, enc) }})
	if ctl != nil {
		gw.SetAdaptive(ctl)
	}
	return gw, mem
}

func chat(gw *Gateway, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-portal-test")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, req)
	return rec
}

const plainReq = `{"model":"coder","messages":[{"role":"user","content":"hi"}]}`
const streamReq = `{"model":"coder","stream":true,"messages":[{"role":"user","content":"hi"}]}`

// routeArm returns the route-level (bucket-less) learned state.
func routeArm(ctl *bridge.Controller, route string) (bandit.ArmSnapshot, bool) {
	for _, a := range ctl.Bandit().Snapshot().Arms {
		if a.Arm.Route == route && a.Arm.Bucket == "" {
			return a, true
		}
	}
	return bandit.ArmSnapshot{}, false
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestAdaptiveIsOffByDefault(t *testing.T) {
	gw, _ := adaptiveGateway(t, nil, fakeRoute{id: "r1", handler: okHandler(0)})
	if gw.Adaptive != nil || gw.Rotator.WeightFn != nil {
		t.Fatal("adaptive must be off unless SetAdaptive is called")
	}
	if rec := chat(gw, plainReq, nil); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	gw.SetAdaptive(nil) // disabling again must be safe
}

// One subtest per observation point in HandleChat.
func TestObservationPoints(t *testing.T) {
	emptyStream := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: [DONE]\n\n"
	goodStream := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	cases := []struct {
		name          string
		handler       http.HandlerFunc
		req           string
		wantS, wantF  float64
		wantArm       bool
		wantLatencyOK bool
	}{
		{"success", okHandler(20 * time.Millisecond), plainReq, 1, 0, true, true},
		{"success on a stream drops latency", sseHandler(goodStream), streamReq, 1, 0, true, false},
		{"upstream 500", statusHandler(0, 500, `{"error":{"message":"boom"}}`), plainReq, 0, 1, true, false},
		{"upstream 429", statusHandler(0, 429, `{"error":{"message":"slow down"}}`), plainReq, 0, 0.3, true, false},
		{"validator failure (empty content)", statusHandler(0, 200, `{"id":"x","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`), plainReq, 0, 0.7, true, false},
		{"empty stream", sseHandler(emptyStream), streamReq, 0, 0.7, true, false},
		{"client error is ignored", statusHandler(0, 400, `{"error":{"message":"bad","param":"foo"}}`), plainReq, 0, 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctl := bridge.New(bridge.Options{Mode: bridge.On})
			gw, _ := adaptiveGateway(t, ctl, fakeRoute{id: "r1", handler: c.handler})
			chat(gw, c.req, nil)
			a, ok := routeArm(ctl, "r1")
			if ok != c.wantArm {
				t.Fatalf("learned arm present=%v, want %v", ok, c.wantArm)
			}
			if !ok {
				return
			}
			if !near(a.S, c.wantS) || !near(a.F, c.wantF) {
				t.Fatalf("evidence S=%v F=%v, want S=%v F=%v", a.S, a.F, c.wantS, c.wantF)
			}
			if (a.LatN > 0) != c.wantLatencyOK {
				t.Fatalf("latency samples %v, want recorded=%v", a.LatN, c.wantLatencyOK)
			}
		})
	}
}

// Two slow failures followed by a fast success: the winner's latency must be
// its own ~50ms, not the ~650ms the whole request took.
func TestFailoverRecordsPerAttemptLatency(t *testing.T) {
	ctl := bridge.New(bridge.Options{Mode: bridge.On})
	gw, _ := adaptiveGateway(t, ctl,
		fakeRoute{id: "r1", handler: statusHandler(300*time.Millisecond, 500, `{"error":{"message":"x"}}`)},
		fakeRoute{id: "r2", handler: statusHandler(300*time.Millisecond, 500, `{"error":{"message":"x"}}`)},
		fakeRoute{id: "r3", handler: okHandler(50 * time.Millisecond)},
	)
	start := time.Now()
	if rec := chat(gw, plainReq, nil); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	total := time.Since(start)
	if total < 600*time.Millisecond {
		t.Fatalf("test setup: request took only %v, failovers did not happen", total)
	}
	a, ok := routeArm(ctl, "r3")
	if !ok || a.LatN == 0 {
		t.Fatalf("no latency recorded for the winning route: %+v", a)
	}
	got := time.Duration(math.Exp(a.LatMean) * float64(time.Second))
	if got < 30*time.Millisecond || got > 250*time.Millisecond {
		t.Fatalf("recorded latency %v for a ~50ms attempt (whole request took %v)", got, total)
	}
	for _, id := range []string{"r1", "r2"} {
		if f, ok := routeArm(ctl, id); !ok || !near(f.F, 1) {
			t.Fatalf("%s: each failed attempt must count once, got %+v", id, f)
		}
	}
}

func TestPrivateRoutesAreNeverLearned(t *testing.T) {
	ctl := bridge.New(bridge.Options{Mode: bridge.On})
	gw, mem := adaptiveGateway(t, ctl, fakeRoute{id: "p1", handler: okHandler(0), private: true})
	if _, err := mem.UpdateConfig(map[string]interface{}{"privacyMode": "low"}); err != nil {
		t.Fatal(err)
	}
	if rec := chat(gw, plainReq, map[string]string{"X-Router-Private": "true"}); rec.Code != 200 {
		t.Fatalf("private request status %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(ctl.Bandit().Snapshot().Arms); n != 0 {
		t.Fatalf("a private route was learned (%d arms)", n)
	}
}

// With adaptive off, or in shadow mode, the sequence of chosen routes must be
// exactly the one a gateway without adaptive rotation produces.
func TestOffAndShadowChooseExactlyLikeNoAdaptive(t *testing.T) {
	run := func(ctl *bridge.Controller) []string {
		gw, mem := adaptiveGateway(t, ctl,
			fakeRoute{id: "r1", handler: okHandler(0)}, fakeRoute{id: "r2", handler: okHandler(0)},
			fakeRoute{id: "r3", handler: okHandler(0)}, fakeRoute{id: "r4", handler: okHandler(0)})
		for i := 0; i < 60; i++ {
			if rec := chat(gw, plainReq, nil); rec.Code != 200 {
				t.Fatalf("request %d: status %d", i, rec.Code)
			}
		}
		rows, _ := mem.ListAudit(100, 0) // newest first
		seq := make([]string, len(rows))
		for i, r := range rows {
			seq[len(rows)-1-i] = r.SelectedRouteID
		}
		return seq
	}
	base := run(nil)
	if len(base) != 60 {
		t.Fatalf("baseline recorded %d requests", len(base))
	}
	for name, ctl := range map[string]*bridge.Controller{
		"off":    bridge.New(bridge.Options{Mode: bridge.Off}),
		"shadow": bridge.New(bridge.Options{Mode: bridge.Shadow, Logf: func(string, ...any) {}}),
	} {
		got := run(ctl)
		for i := range base {
			if got[i] != base[i] {
				t.Fatalf("%s: request %d went to %s, baseline went to %s", name, i, got[i], base[i])
			}
		}
	}
}

func rotatorOnlyGateway(ctl *bridge.Controller) *Gateway {
	gw := New(store.NewMemStore(), Config{})
	gw.SetAdaptive(ctl)
	return gw
}

func fourCandidates() []scheduler.Candidate {
	var c []scheduler.Candidate
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("r%d", i)
		c = append(c, scheduler.Candidate{Route: scheduler.Route{ID: id, ModelID: "m-" + id}, Key: scheduler.ProviderKey{ID: "k-" + id}})
	}
	return c
}

func shares(gw *Gateway, n int) map[string]float64 {
	c := map[string]int{}
	for i := 0; i < n; i++ {
		c[gw.Rotator.ReorderForRotation(fourCandidates(), "coder", scheduler.SizeSmall, "", nil)[0].Route.ID]++
	}
	out := map[string]float64{}
	for k, v := range c {
		out[k] = float64(v) / float64(n)
	}
	return out
}

// The failure of the first attempt: with no learning data every weight became
// zero and all picks went to one route. Through the real Rotator.
func TestColdStartOnMatchesStaticThroughRotator(t *testing.T) {
	gw := rotatorOnlyGateway(bridge.New(bridge.Options{Mode: bridge.On}))
	s := shares(gw, 1000)
	for id, want := range map[string]float64{"r0": 0.4, "r1": 0.3, "r2": 0.2, "r3": 0.1} {
		if math.Abs(s[id]-want) > 0.02 {
			t.Errorf("%s share %.3f, want %.2f +/- 0.02 (all: %v)", id, s[id], want, s)
		}
	}
}

func TestLearnedFailuresShiftTrafficButKeepProbing(t *testing.T) {
	ctl := bridge.New(bridge.Options{Mode: bridge.On})
	gw := rotatorOnlyGateway(ctl)
	for i := 0; i < 60; i++ {
		ctl.Observe("r0", "small", false, "server_error", 0, false)
		for _, id := range []string{"r1", "r2", "r3"} {
			ctl.Observe(id, "small", false, "ok", time.Second, false)
		}
	}
	s := shares(gw, 2000)
	if !(s["r0"] < 0.25) {
		t.Errorf("failing top route kept %.3f of traffic, want < 0.25 (static is 0.40)", s["r0"])
	}
	if !(s["r0"] > 0.02) {
		t.Errorf("failing top route got %.3f: it must keep a probing share so it can recover", s["r0"])
	}
}

func TestConcurrentRequestsWithAdaptiveOn(t *testing.T) {
	ctl := bridge.New(bridge.Options{Mode: bridge.On})
	gw, mem := adaptiveGateway(t, ctl,
		fakeRoute{id: "r1", handler: okHandler(0)}, fakeRoute{id: "r2", handler: okHandler(0)},
		fakeRoute{id: "r3", handler: statusHandler(0, 500, `{"error":{"message":"x"}}`)})
	// 160 requests in a few milliseconds would hit the gateway's own 120 RPM client limit.
	if _, err := mem.UpdateConfig(map[string]interface{}{"rateLimitRpm": float64(100000)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	bad := make(chan int, 200)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if rec := chat(gw, plainReq, nil); rec.Code != 200 {
					bad <- rec.Code
				}
			}
		}()
	}
	wg.Wait()
	close(bad)
	for code := range bad {
		t.Fatalf("a request failed with %d although two routes are healthy", code)
	}
}
