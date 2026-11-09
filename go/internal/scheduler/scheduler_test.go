package scheduler

import (
	"testing"
)

func intPtr(v int) *int { return &v }

func TestEstimateTokens(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "hello world"}}
	// ascii 11 * 0.28 * 1.10 = 3.388 -> ceil 4
	if got := EstimateTokens(msgs, 0); got != 4 {
		t.Fatalf("ascii estimate = %d, want 4", got)
	}
	msgs = []Message{{Role: "user", Content: "你好"}}
	// CJK 2 * 0.75 * 1.10 = 1.65 -> ceil 2
	if got := EstimateTokens(msgs, 0); got != 2 {
		t.Fatalf("CJK estimate = %d, want 2", got)
	}
}

func TestSizeClass(t *testing.T) {
	if SizeClassOf(3200) != SizeSmall || SizeClassOf(3201) != SizeMedium || SizeClassOf(32000) != SizeMedium || SizeClassOf(32001) != SizeLarge {
		t.Fatal("size class boundaries wrong")
	}
}

func TestComputeAliasScore(t *testing.T) {
	// Medium, first target, reliability 100, latency 400, no limit:
	// order 1000 + rel 10 - lat 0.4 + quota 10 = 1019.6
	score, _ := ComputeAliasScore(0, intPtr(100), 400, nil, "verified", 128000, "https://example.com/v1", 10000, "neutral", nil, true)
	if score != 1019.6 {
		t.Fatalf("medium score = %v, want 1019.6", score)
	}
	// Failed demotion -500.
	score, _ = ComputeAliasScore(0, intPtr(100), 400, nil, "failed", 128000, "https://example.com/v1", 10000, "neutral", nil, true)
	if score != 519.6 {
		t.Fatalf("failed score = %v, want 519.6", score)
	}
	// Large + 1M context +25, Groq -200.
	score, _ = ComputeAliasScore(0, intPtr(100), 400, nil, "verified", 1000000, "https://api.groq.com/openai/v1", 50000, "neutral", nil, true)
	if score != 864.6 {
		t.Fatalf("large Groq score = %v, want 864.6", score)
	}
	// Small order gap 50: second target orderBase 950.
	score, _ = ComputeAliasScore(1, intPtr(100), 400, nil, "verified", 128000, "https://example.com/v1", 1000, "neutral", nil, true)
	if score != 967.6 {
		t.Fatalf("small second-target score = %v, want 967.6", score)
	}
	// Capability bonus only when included and language matches.
	score, _ = ComputeAliasScore(0, intPtr(100), 400, nil, "verified", 128000, "https://example.com/v1", 10000, "chinese", []string{"chinese"}, true)
	if score != 1069.6 {
		t.Fatalf("capability score = %v, want 1069.6", score)
	}
	score, _ = ComputeAliasScore(0, intPtr(100), 400, nil, "verified", 128000, "https://example.com/v1", 10000, "chinese", []string{"chinese"}, false)
	if score != 1019.6 {
		t.Fatalf("display score must exclude capability bonus, got %v", score)
	}
}

func TestRotationWRR(t *testing.T) {
	r := NewRotator()
	cands := []Candidate{
		{Route: Route{ID: "r1", ModelID: "m1"}, Key: ProviderKey{ID: "k1"}},
		{Route: Route{ID: "r2", ModelID: "m2"}, Key: ProviderKey{ID: "k2"}},
		{Route: Route{ID: "r3", ModelID: "m3"}, Key: ProviderKey{ID: "k3"}},
	}
	// Weights 4/3/2: first pick r1, then r2, then r1 again in smooth WRR
	// (Node reorderCandidatesForRotation). At minimum, picks must rotate
	// and must not always be r1.
	picks := []string{}
	for i := 0; i < 9; i++ {
		out := r.ReorderForRotation(cands, "coder", SizeMedium, "", map[string]int{})
		picks = append(picks, out[0].Route.ID)
	}
	seen := map[string]bool{}
	for _, p := range picks {
		seen[p] = true
	}
	if len(seen) < 2 {
		t.Fatalf("rotation did not rotate, picks = %v", picks)
	}
	if picks[0] != "r1" {
		t.Fatalf("first pick = %s, want r1 (highest weight first)", picks[0])
	}
	// Failover order: picked first, rest in original order.
	out := NewRotator().ReorderForRotation(cands, "coder", SizeMedium, "", map[string]int{})
	if out[0].Route.ID != "r1" || out[1].Route.ID != "r2" || out[2].Route.ID != "r3" {
		t.Fatalf("failover order wrong: %v", out)
	}
}

func TestRotationKaggleCapAndGroqLarge(t *testing.T) {
	r := NewRotator()
	cands := []Candidate{
		{Route: Route{ID: "rk", ModelID: "kaggle-model", BaseURL: "https://mp-staging.kaggle.net/v1"}, Key: ProviderKey{ID: "kk"}},
		{Route: Route{ID: "r2", ModelID: "m2", BaseURL: "https://example.com/v1"}, Key: ProviderKey{ID: "k2"}},
	}
	// Kaggle share 90/100 = 90% > 60%: Kaggle leaves rotation first-pick,
	// but stays in failover (position 2).
	out := r.ReorderForRotation(cands, "coder", SizeMedium, "kaggle-model", map[string]int{"kk": 90, "k2": 10})
	if out[0].Route.ID != "r2" {
		t.Fatalf("Kaggle cap: first pick = %s, want r2", out[0].Route.ID)
	}
	if out[1].Route.ID != "rk" {
		t.Fatalf("Kaggle cap: Kaggle must stay in failover, got %v", out)
	}
	// Large: Groq leaves rotation pool.
	r2 := NewRotator()
	cands2 := []Candidate{
		{Route: Route{ID: "rg", ModelID: "groq-model", BaseURL: "https://api.groq.com/openai/v1"}, Key: ProviderKey{ID: "kg"}},
		{Route: Route{ID: "r2", ModelID: "m2", BaseURL: "https://example.com/v1"}, Key: ProviderKey{ID: "k2"}},
	}
	out2 := r2.ReorderForRotation(cands2, "coder", SizeLarge, "", map[string]int{})
	if out2[0].Route.ID != "r2" {
		t.Fatalf("large Groq exclusion: first pick = %s, want r2", out2[0].Route.ID)
	}
}

func TestHardFilter(t *testing.T) {
	cls := Classification{HasImage: true, EstimatedTokens: 1000}
	route := Route{SupportsVision: false, SupportsTools: true, SupportsJSONMode: true, MaxContext: 128000}
	if HardFilter(cls, route, nil) {
		t.Fatal("image request must not go to a non-vision route")
	}
	route.SupportsVision = true
	if !HardFilter(cls, route, nil) {
		t.Fatal("vision route must pass")
	}
	cls2 := Classification{IsPrivate: true, EstimatedTokens: 1000}
	route2 := Route{SupportsVision: true, SupportsTools: true, SupportsJSONMode: true, MaxContext: 128000, AllowSensitive: false}
	if HardFilter(cls2, route2, nil) {
		t.Fatal("private request must not go to a non-sensitive route")
	}
	route3 := Route{SupportsVision: true, SupportsTools: true, SupportsJSONMode: true, MaxContext: 1500}
	cls3 := Classification{EstimatedTokens: 1000}
	if HardFilter(cls3, route3, nil) {
		t.Fatal("maxContext < estimated+1000 must be filtered")
	}
}

func TestClassify(t *testing.T) {
	cls := Classify(ClassifyInput{Model: "coder", Messages: []Message{{Role: "user", Content: "hi"}}, PrivacyMode: "off", RequestID: "x"})
	if cls.TaskType != "fast" || cls.RoutingMode != "pool" {
		t.Fatalf("small coder request: %+v", cls)
	}
	cls = Classify(ClassifyInput{Model: "auto:vision", Messages: []Message{{Role: "user", Content: "hi"}}, PrivacyMode: "off", RequestID: "x"})
	if cls.TaskType != "vision" || cls.RoutingMode != "auto" {
		t.Fatalf("auto:vision: %+v", cls)
	}
	cls = Classify(ClassifyInput{Model: "coder", Messages: []Message{{Role: "user", Content: "hi"}}, HasTools: true, ToolsJSONLen: 100, PrivacyMode: "off", RequestID: "x"})
	if cls.TaskType != "reasoning" {
		t.Fatalf("tools request must be reasoning, got %s", cls.TaskType)
	}
	cls = Classify(ClassifyInput{Model: "coder", Messages: []Message{{Role: "user", Content: "hi"}}, PrivateHeader: "true", PrivacyMode: "off", RequestID: "x"})
	if cls.IsPrivate {
		t.Fatal("privacy off must ignore the private header")
	}
	cls = Classify(ClassifyInput{Model: "coder", Messages: []Message{{Role: "user", Content: "hi"}}, PrivateHeader: "true", PrivacyMode: "high", RequestID: "x"})
	if !cls.IsPrivate {
		t.Fatal("privacy high must honour the private header")
	}
}
