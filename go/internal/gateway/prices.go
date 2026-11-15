package gateway

import (
	_ "embed"
	"encoding/json"
	"math"
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

// LookupModelPrice returns the ModelPrice metadata for a given modelID if present.
func LookupModelPrice(modelID string) (ModelPrice, bool) {
	if modelID == "" {
		return ModelPrice{}, false
	}
	tbl := loadPriceTable()
	p, ok := tbl[modelID]
	return p, ok
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
