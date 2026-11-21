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

func TestLookupModelPriceNumes(t *testing.T) {
	// Numes $0 models (2026-11-16): 5 priced exactly from the live LiteLLM
	// file, 4 third-party fine-tunes priced at their base model's rate as
	// an estimate. All 9 must resolve so Numes traffic shows in /cost/.
	exact := map[string][2]float64{
		"deepseek/deepseek-v4.1-flash": {0.0000003, 0.0000012},
		"z-ai/glm-5.3":                 {0.0000014, 0.0000044},
		"z-ai/glm-5.3-flash":           {0.00000015, 0.0000005},
		"moonshotai/kimi-k3.1-pro":     {0.000002, 0.00001},
		"nvidia/nemotron-3-ultra":      {0.0000006, 0.0000024},
	}
	for id, want := range exact {
		p, ok := LookupModelPrice(id)
		if !ok {
			t.Fatalf("expected ok=true for %s", id)
		}
		if p.InputCostPerToken != want[0] || p.OutputCostPerToken != want[1] {
			t.Fatalf("expected %s prices (%v/%v), got %v/%v", id, want[0], want[1], p.InputCostPerToken, p.OutputCostPerToken)
		}
		if p.Estimated {
			t.Fatalf("expected Estimated=false for %s (exact price)", id)
		}
	}
	estimated := map[string][2]float64{
		"dealignai/deepseek-v4.1-flash-uncensored-fp8": {0.0000003, 0.0000012},
		"dealignai/glm-5.3-flash-uncensored-fp8":       {0.00000015, 0.0000005},
		"dealignai/glm-5.3-uncensored-fp8":             {0.0000014, 0.0000044},
		"uniboshi/kimi-k3-abliterated-v1":              {0.000003, 0.000015},
	}
	for id, want := range estimated {
		p, ok := LookupModelPrice(id)
		if !ok {
			t.Fatalf("expected ok=true for %s", id)
		}
		if p.InputCostPerToken != want[0] || p.OutputCostPerToken != want[1] {
			t.Fatalf("expected %s prices (%v/%v), got %v/%v", id, want[0], want[1], p.InputCostPerToken, p.OutputCostPerToken)
		}
		if !p.Estimated {
			t.Fatalf("expected Estimated=true for %s (base-model estimate)", id)
		}
	}
	// Spot-check CostFor: glm-5.3-flash 1M in + 500K out = 0.15 + 0.25 = $0.40
	cost, ok := CostFor("z-ai/glm-5.3-flash", 1_000_000, 500_000)
	if !ok {
		t.Fatalf("expected ok=true for z-ai/glm-5.3-flash cost")
	}
	if math.Abs(cost-0.40) > 0.001 {
		t.Fatalf("expected cost ~0.40, got %.6f", cost)
	}
}

func TestLookupModelPrice100milionseek(t *testing.T) {
	// 100milionseek serves DeepSeek V4.1 Flash under the bare id
	// (no deepseek/ prefix, hyphenated -free suffix). LiteLLM lists
	// it as deepseek/deepseek-v4.1-flash (3e-07 in / 1.2e-06 out),
	// so this is an exact price, not an estimate. Without the alias
	// the route's cost_usd stays NULL and /cost/ drops the model.
	p, ok := LookupModelPrice("deepseek-v4.1-flash-free")
	if !ok {
		t.Fatalf("expected ok=true for deepseek-v4.1-flash-free")
	}
	if p.InputCostPerToken != 0.0000003 || p.OutputCostPerToken != 0.0000012 {
		t.Fatalf("expected DeepSeek V4.1 Flash prices (3e-07/1.2e-06), got %v/%v",
			p.InputCostPerToken, p.OutputCostPerToken)
	}
	if p.Estimated {
		t.Fatalf("expected Estimated=false for an exact canonical price")
	}
	// 1M in + 500K out: 0.30 + 0.60 = $0.90
	cost, ok := CostFor("deepseek-v4.1-flash-free", 1_000_000, 500_000)
	if !ok {
		t.Fatalf("expected ok=true for deepseek-v4.1-flash-free cost")
	}
	if math.Abs(cost-0.90) > 0.001 {
		t.Fatalf("expected cost ~0.90, got %.6f", cost)
	}
}
