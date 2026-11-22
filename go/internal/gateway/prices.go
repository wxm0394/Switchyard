package gateway

import (
	_ "embed"
	"encoding/json"
	"math"
	"strings"
	"sync"
)

//go:embed prices.json
var embeddedPricesJSON []byte

// ModelPrice holds per-token USD market reference pricing from LiteLLM
// (https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json).
type ModelPrice struct {
	LiteLLMKey         string  `json:"litellm_key"`
	LiteLLMProvider    string  `json:"litellm_provider"`
	InputCostPerToken  float64 `json:"input_cost_per_token"`
	OutputCostPerToken float64 `json:"output_cost_per_token"`
	Estimated          bool    `json:"estimated"`
	FreeRoute          bool    `json:"free_route"`
}

var (
	priceTableOnce sync.Once
	priceTable     map[string]ModelPrice
)

func loadPriceTable() map[string]ModelPrice {
	priceTableOnce.Do(func() {
		priceTable = make(map[string]ModelPrice)
		if len(embeddedPricesJSON) > 0 {
			_ = json.Unmarshal(embeddedPricesJSON, &priceTable)
		}
	})
	return priceTable
}

// priceAlias maps a gateway model_id whose LiteLLM key differs from the
// gateway id to the canonical key in prices.json. estimated=true means the
// mapping is an approximation (nearest hosted equivalent, not the same
// model); the panel shows it as 估算. Verified against the live LiteLLM
// price file 2026-11-15; models with no LiteLLM listing at all
// (poolside/laguna-xs-2.1, cohere/north-mini-code, Kaggle-internal
// Qwen3.5/Qwen3.8, TEMP probe ids) are deliberately left unmapped and
// stay price-unknown (NULL) rather than priced on a guess.
type priceAlias struct {
	canonical string
	estimated bool
}

var priceAliases = map[string]priceAlias{
	// xKiro serves Codestral under the mistralai/ prefix; LiteLLM lists it as mistral/.
	"mistralai/codestral-2508": {canonical: "mistral/codestral-2508"},
	// The old Zhipu-direct route used the bare id; LiteLLM lists GLM-4.7 under zai/.
	"glm-4.7": {canonical: "zai/glm-4.7"},
	// Gateway id keeps the vendor prefix + original case; LiteLLM uses moonshot/ lowercase.
	"moonshotai/Kimi-K3.1-Pro": {canonical: "moonshot/kimi-k3.1-pro"},
	// Self-hosted Qwen2.5-Coder-14B (local GGUF) has no LiteLLM listing at
	// any size: 14B exists nowhere in the price file. Nearest hosted Qwen
	// coder price (Hyperbolic 32B) is used as an estimate, marked as such.
	"qwen2.5-coder-14b": {canonical: "hyperbolic/Qwen/Qwen2.5-Coder-32B-Instruct", estimated: true},
	// Numes serves Kimi K3.1 Pro under a lowercase moonshotai/ prefix;
	// LiteLLM lists it under moonshot/.
	"moonshotai/kimi-k3.1-pro": {canonical: "moonshot/kimi-k3.1-pro"},
	// Numes id drops the 550b-a55b suffix; same model as the existing
	// nvidia/nemotron-3-ultra-550b-a55b entry (Baseten price).
	"nvidia/nemotron-3-ultra": {canonical: "nvidia/nemotron-3-ultra-550b-a55b"},
	// Third-party uncensored/abliterated fine-tunes have no LiteLLM listing
	// anywhere. Priced at their base model's rate as an estimate, marked so.
	"dealignai/deepseek-v4.1-flash-uncensored-fp8": {canonical: "deepseek/deepseek-v4.1-flash", estimated: true},
	"dealignai/glm-5.3-flash-uncensored-fp8":       {canonical: "z-ai/glm-5.3-flash", estimated: true},
	"dealignai/glm-5.3-uncensored-fp8":             {canonical: "z-ai/glm-5.3", estimated: true},
	"uniboshi/kimi-k3-abliterated-v1":              {canonical: "moonshot/kimi-k3", estimated: true},
	// 100milionseek serves DeepSeek V4.1 Flash under the bare id (no
	// deepseek/ prefix, and a hyphenated -free suffix, so the :free
	// strip in LookupModelPrice never fires); LiteLLM lists it as
	// deepseek/deepseek-v4.1-flash. Without this alias the route's
	// cost_usd stays NULL and /cost/ drops the whole model.
	"deepseek-v4.1-flash-free": {canonical: "deepseek/deepseek-v4.1-flash"},
	// OpenRouter serves GLM-5.3 Flash under the z-ai/ slug, but the
	// gateway route is namespaced openrouter/z-ai/glm-5.3-flash (the
	// LevelNRead dedicated line, alias lnr). LiteLLM lists the
	// canonical z-ai/glm-5.3-flash at 1.5e-07 in / 5e-07 out, so this
	// is an exact price, not an estimate. Without the alias the
	// route's cost_usd stays NULL and /cost/ drops the whole model.
	"openrouter/z-ai/glm-5.3-flash": {canonical: "z-ai/glm-5.3-flash"},
}

// LookupModelPrice returns the ModelPrice metadata for a given modelID if present.
//
// Resolution order: exact key, alias map, then the same two steps with a
// trailing ":free" suffix stripped (free-tier variants are priced at the
// base model's rate, e.g. nvidia/nemotron-3-ultra-550b-a55b:free uses the
// nvidia/nemotron-3-ultra-550b-a55b price). Anything else is unknown.
func LookupModelPrice(modelID string) (ModelPrice, bool) {
	if modelID == "" {
		return ModelPrice{}, false
	}
	tbl := loadPriceTable()
	if p, ok := tbl[modelID]; ok {
		return p, true
	}
	if p, ok := lookupAlias(tbl, modelID); ok {
		return p, true
	}
	if base, ok := strings.CutSuffix(modelID, ":free"); ok {
		if p, ok := tbl[base]; ok {
			return p, true
		}
		if p, ok := lookupAlias(tbl, base); ok {
			return p, true
		}
	}
	return ModelPrice{}, false
}

func lookupAlias(tbl map[string]ModelPrice, modelID string) (ModelPrice, bool) {
	a, ok := priceAliases[modelID]
	if !ok {
		return ModelPrice{}, false
	}
	p, ok := tbl[a.canonical]
	if !ok {
		return ModelPrice{}, false
	}
	if a.estimated {
		p.Estimated = true
	}
	return p, true
}

// CostFor computes the market-reference USD price for a single request:
//
//	cost_usd = promptTokens * input_price + completionTokens * output_price
//
// Returns (0, false) if the model is not found in the LiteLLM price map or if inputs are invalid.
// Never panics.
func CostFor(modelID string, promptTokens, completionTokens int64) (float64, bool) {
	defer func() {
		_ = recover()
	}()
	p, ok := LookupModelPrice(modelID)
	if !ok {
		return 0, false
	}
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	cost := float64(promptTokens)*p.InputCostPerToken + float64(completionTokens)*p.OutputCostPerToken
	if math.IsNaN(cost) || math.IsInf(cost, 0) {
		return 0, false
	}
	return cost, true
}
