package scheduler

import (
	"fmt"
	"math"
	"sync"
	"testing"
)

func hookPool(n int) []Candidate {
	var c []Candidate
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("r%d", i)
		c = append(c, Candidate{Route: Route{ID: id, ModelID: "m-" + id}, Key: ProviderKey{ID: "k-" + id}})
	}
	return c
}

func picks(r *Rotator, pool []Candidate, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = r.ReorderForRotation(pool, "p", SizeSmall, "", nil)[0].Route.ID
	}
	return out
}

func count(ids []string) map[string]int {
	m := map[string]int{}
	for _, id := range ids {
		m[id]++
	}
	return m
}

func TestWeightFnUnsetKeepsStaticDistribution(t *testing.T) {
	c := count(picks(NewRotator(), hookPool(4), 1000))
	if c["r0"] != 400 || c["r1"] != 300 || c["r2"] != 200 || c["r3"] != 100 {
		t.Fatalf("static 4:3:2:1 expected over 1000 picks, got %v", c)
	}
}

func TestWeightFnReceivesPoolAndStaticWeights(t *testing.T) {
	r := NewRotator()
	var gotKey string
	var gotSize SizeClass
	var gotIDs []string
	var gotStatic []float64
	r.WeightFn = func(poolKey string, size SizeClass, pool []Candidate, static []float64) []float64 {
		gotKey, gotSize, gotStatic = poolKey, size, static
		gotIDs = nil
		for _, c := range pool {
			gotIDs = append(gotIDs, c.Route.ID)
		}
		return nil
	}
	r.ReorderForRotation(hookPool(4), "coder", SizeMedium, "", nil)
	if gotKey != "coder" || gotSize != SizeMedium {
		t.Fatalf("poolKey/size = %q/%q", gotKey, gotSize)
	}
	if fmt.Sprint(gotIDs) != "[r0 r1 r2 r3]" || fmt.Sprint(gotStatic) != "[4 3 2 1]" {
		t.Fatalf("pool %v static %v", gotIDs, gotStatic)
	}
}

func TestWeightFnStaticSliceIsACopy(t *testing.T) {
	r := NewRotator()
	r.WeightFn = func(_ string, _ SizeClass, _ []Candidate, static []float64) []float64 {
		for i := range static {
			static[i] = 100 // a careless hook must not corrupt the rotator's own weights
		}
		return nil
	}
	want := count(picks(NewRotator(), hookPool(4), 1000))
	got := count(picks(r, hookPool(4), 1000))
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Fatalf("hook mutated static weights: want %v got %v", want, got)
	}
}

// Anything unusable must give exactly the static behaviour, never an error.
func TestWeightFnUnusableResultsFallBackToStatic(t *testing.T) {
	cases := map[string]func() []float64{
		"nil":          func() []float64 { return nil },
		"too short":    func() []float64 { return []float64{1, 1, 1} },
		"too long":     func() []float64 { return []float64{1, 1, 1, 1, 1} },
		"NaN":          func() []float64 { return []float64{1, math.NaN(), 1, 1} },
		"+Inf":         func() []float64 { return []float64{1, math.Inf(1), 1, 1} },
		"zero":         func() []float64 { return []float64{1, 0, 1, 1} },
		"negative":     func() []float64 { return []float64{1, -2, 1, 1} },
		"panics":       func() []float64 { panic("boom") },
		"empty slice":  func() []float64 { return []float64{} },
		"all zero":     func() []float64 { return []float64{0, 0, 0, 0} },
		"neg infinity": func() []float64 { return []float64{math.Inf(-1), 1, 1, 1} },
	}
	want := picks(NewRotator(), hookPool(4), 400)
	for name, fn := range cases {
		r := NewRotator()
		fn := fn
		r.WeightFn = func(string, SizeClass, []Candidate, []float64) []float64 { return fn() }
		got := picks(r, hookPool(4), 400)
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: pick %d = %s, static gives %s", name, i, got[i], want[i])
				break
			}
		}
	}
}

func TestWeightFnValidWeightsAreUsed(t *testing.T) {
	r := NewRotator()
	r.WeightFn = func(string, SizeClass, []Candidate, []float64) []float64 { return []float64{1, 1, 1, 1} }
	c := count(picks(r, hookPool(4), 1000))
	for id, n := range c {
		if n != 250 {
			t.Fatalf("equal weights should give 250 each, %s got %d (%v)", id, n, c)
		}
	}
	r2 := NewRotator()
	r2.WeightFn = func(string, SizeClass, []Candidate, []float64) []float64 { return []float64{0.5, 0.25, 0.25, 0.25} }
	c2 := count(picks(r2, hookPool(4), 1000))
	if c2["r0"] < 395 || c2["r0"] > 405 || c2["r1"] < 195 || c2["r1"] > 205 {
		t.Fatalf("fractional weights 0.5/0.25/0.25/0.25 should give about 400/200/200/200, got %v", c2)
	}
}

// The hook is called outside the rotator lock, so it may use the rotator.
func TestWeightFnRunsWithoutHoldingTheRotatorLock(t *testing.T) {
	r := NewRotator()
	inner := false
	r.WeightFn = func(string, SizeClass, []Candidate, []float64) []float64 {
		if !inner {
			inner = true
			r.ReorderForRotation(hookPool(2), "other", SizeSmall, "", nil) // would deadlock if the lock were held
		}
		return nil
	}
	r.ReorderForRotation(hookPool(4), "p", SizeSmall, "", nil)
}

func TestWeightFnConcurrent(t *testing.T) {
	r := NewRotator()
	r.WeightFn = func(_ string, _ SizeClass, pool []Candidate, static []float64) []float64 {
		w := make([]float64, len(static))
		for i := range w {
			w[i] = static[i] * 1.5
		}
		return w
	}
	pool := hookPool(4)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				r.ReorderForRotation(pool, "p", SizeSmall, "", nil)
			}
		}()
	}
	wg.Wait()
}
