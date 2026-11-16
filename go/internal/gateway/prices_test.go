package gateway

import (
	"math"
	"testing"
)

func TestLookupModelPriceFreeSuffix(t *testing.T) {
	// Kilo serves Ultra under the :free id; it must price at the base
	// model's LiteLLM rate (66M+ tokens/week ride on this one mapping).
	p, ok := LookupModelPrice("nvidia/nemotron-3-ultra-550b-a55b:free")
	if !ok {
		t.Fatalf("expected ok=true for Ultra :free variant")
	}
	if p.InputCostPerToken != 0.0000006 || p.OutputCostPerToken != 0.0000024 {
		t.Fatalf("expected Ultra base prices (6e-07/2.4e-06), got %v/%v",
			p.InputCostPerToken, p.OutputCostPerToken)
	}
	if p.Estimated {
		t.Fatalf("expected Estimated=false for an exact base-model price")
	}
	// Base id itself is unchanged.
	base, ok := LookupModelPrice("nvidia/nemotron-3-ultra-550b-a55b")
	if !ok || base != p {
		t.Fatalf("base Ultra lookup changed: ok=%v base=%+v free=%+v", ok, base, p)
	}
}

func TestLookupModelPriceAliases(t *testing.T) {
	cases := []struct {
		modelID   string
		wantIn    float64
		wantOut   float64
		estimated bool
	}{
		{"mistralai/codestral-2508", 0.0000003, 0.0000009, false},
		{"glm-4.7", 0.0000006, 0.0000022, false},
		{"moonshotai/Kimi-K3.1-Pro", 0.000002, 0.00001, false},
		// 14B has no LiteLLM listing: nearest hosted Qwen coder (Hyperbolic 32B), estimated.
		{"qwen2.5-coder-14b", 0.00000012, 0.0000003, true},
	}
	for _, tc := range cases {
		p, ok := LookupModelPrice(tc.modelID)
		if !ok {
			t.Fatalf("expected ok=true for %s", tc.modelID)
		}
		if p.InputCostPerToken != tc.wantIn || p.OutputCostPerToken != tc.wantOut {
			t.Fatalf("%s: expected %v/%v, got %v/%v",
				tc.modelID, tc.wantIn, tc.wantOut, p.InputCostPerToken, p.OutputCostPerToken)
		}
		if p.Estimated != tc.estimated {
			t.Fatalf("%s: expected Estimated=%v, got %v", tc.modelID, tc.estimated, p.Estimated)
		}
	}
}

func TestLookupModelPriceStillUnknown(t *testing.T) {
	// No LiteLLM listing exists for these at any key: they must stay
	// price-unknown (NULL), never priced on a guess.
	for _, id := range []string{
		"poolside/laguna-xs-2.1:free",
		"poolside/laguna-xs-2.1",
		"cohere/north-mini-code:free",
		"Qwen/Qwen3.5-397B-A17B",
		"Qwen/Qwen3.8-27B",
		"Qwen/Qwen3.8-Flash-Next",
		"TEMP-probe-1794447805",
		"kilo-auto/free",
		"",
	} {
		if _, ok := LookupModelPrice(id); ok {
			t.Fatalf("expected ok=false for %q", id)
		}
	}
}

func TestCostForFreeSuffixAndAlias(t *testing.T) {
	// Ultra :free, 1M in + 500K out: 1.0*0.6 + 0.5*2.4 = $1.80
	cost, ok := CostFor("nvidia/nemotron-3-ultra-550b-a55b:free", 1_000_000, 500_000)
	if !ok {
		t.Fatalf("expected ok=true for Ultra :free")
	}
	if math.Abs(cost-1.80) > 0.001 {
		t.Fatalf("expected cost ~1.80, got %.6f", cost)
	}
	// Qwen 14B estimate, 1M in + 500K out: 0.12 + 0.15 = $0.27
	cost, ok = CostFor("qwen2.5-coder-14b", 1_000_000, 500_000)
	if !ok {
		t.Fatalf("expected ok=true for qwen2.5-coder-14b")
	}
	if math.Abs(cost-0.27) > 0.001 {
		t.Fatalf("expected cost ~0.27, got %.6f", cost)
	}
}

func TestLookupModelPriceGPT6Luna(t *testing.T) {
	// Coarena's only free model; LiteLLM lists the bare id exactly
	// (gpt-6-luna: 1e-07 in / 5e-07 out), so this is an exact price,
	// not an estimate. It must price so Luna traffic shows in /cost/.
	p, ok := LookupModelPrice("gpt-6-luna")
	if !ok {
		t.Fatalf("expected ok=true for gpt-6-luna")
	}
	if p.InputCostPerToken != 0.0000001 || p.OutputCostPerToken != 0.0000005 {
		t.Fatalf("expected Luna prices (1e-07/5e-07), got %v/%v",
			p.InputCostPerToken, p.OutputCostPerToken)
	}
	if p.Estimated {
		t.Fatalf("expected Estimated=false for an exact LiteLLM listing")
	}
	// 1M in + 500K out: 0.10 + 0.25 = $0.35
	cost, ok := CostFor("gpt-6-luna", 1_000_000, 500_000)
	if !ok {
		t.Fatalf("expected ok=true for gpt-6-luna cost")
	}
	if math.Abs(cost-0.35) > 0.001 {
		t.Fatalf("expected cost ~0.35, got %.6f", cost)
	}
}
