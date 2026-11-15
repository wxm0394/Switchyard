package gateway

import (
	"database/sql"
	"math"
	"sort"
	"time"
)

const DefaultHistoryDBPath = "/opt/switchyard-history/audit_history.db"

type CostModelBreakdown struct {
	ModelID      string   `json:"model_id"`
	CostUSD      *float64 `json:"cost_usd"`
	Tokens       int64    `json:"tokens"`
	Requests     int64    `json:"requests"`
	Estimated    bool     `json:"estimated,omitempty"`
	PriceUnknown bool     `json:"price_unknown,omitempty"`
}

type CostDayBreakdown struct {
	Date     string  `json:"date"`
	CostUSD  float64 `json:"cost_usd"`
	Tokens   int64   `json:"tokens"`
	Requests int64   `json:"requests"`
}

type CostClientBreakdown struct {
	ClientTokenName string  `json:"client_token_name"`
	CostUSD         float64 `json:"cost_usd"`
	Tokens          int64   `json:"tokens"`
	Requests        int64   `json:"requests"`
}

type CostSummaryResponse struct {
	Range              string                `json:"range"`
	TotalCostUSD       float64               `json:"total_cost_usd"`
	TotalTokens        int64                 `json:"total_tokens"`
	TotalRequests      int64                 `json:"total_requests"`
	ByModel            []CostModelBreakdown  `json:"by_model"`
	ByDay              []CostDayBreakdown    `json:"by_day"`
	ByClient           []CostClientBreakdown `json:"by_client"`
	UnknownPriceTokens int64                 `json:"unknown_price_tokens"`
	Note               string                `json:"note"`
}

func roundUSD2(v float64) float64 {
	return math.Round(v*100) / 100
}

// cstCutoffUTC computes the UTC start timestamp for a CST-day (UTC+8) window:
// CST day = previous UTC day 16:00:00 to current UTC day 16:00:00.
func cstCutoffUTC(nowUTC time.Time, rangeParam string) (string, time.Time) {
	normalized := "7d"
	days := 7
	switch rangeParam {
	case "today":
		normalized = "today"
		days = 1
	case "30d":
		normalized = "30d"
		days = 30
	case "7d":
		normalized = "7d"
		days = 7
	}
	cstZone := time.FixedZone("CST", 8*3600)
	nowCST := nowUTC.In(cstZone)
	todayStartCST := time.Date(nowCST.Year(), nowCST.Month(), nowCST.Day(), 0, 0, 0, 0, cstZone)
	cutoffCST := todayStartCST.AddDate(0, 0, -(days - 1))
	return normalized, cutoffCST.UTC()
}

// QueryCostSummary queries audit_history (or fallback audit_logs) and aggregates market-reference costs.
func QueryCostSummary(db *sql.DB, tableName string, rangeParam string, nowUTC time.Time) (CostSummaryResponse, error) {
	normalizedRange, cutoffUTC := cstCutoffUTC(nowUTC, rangeParam)
	cutoffISO := cutoffUTC.Format(time.RFC3339)

	resp := CostSummaryResponse{
		Range:    normalizedRange,
		ByModel:  []CostModelBreakdown{},
		ByDay:    []CostDayBreakdown{},
		ByClient: []CostClientBreakdown{},
		Note:     "参考价，按市价估算，非实际扣费",
	}

	// 1. Totals & unknown_price_tokens
	totalsQuery := `
		SELECT
			COALESCE(SUM(CASE WHEN cost_usd IS NOT NULL THEN cost_usd ELSE 0 END), 0),
			COALESCE(SUM(COALESCE(billed_tokens, 0)), 0),
			COUNT(*),
			COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN COALESCE(billed_tokens, 0) ELSE 0 END), 0)
		FROM ` + tableName + `
		WHERE timestamp >= ?
	`
	var rawTotalCost float64
	if err := db.QueryRow(totalsQuery, cutoffISO).Scan(
		&rawTotalCost,
		&resp.TotalTokens,
		&resp.TotalRequests,
		&resp.UnknownPriceTokens,
	); err != nil {
		return resp, err
	}
	resp.TotalCostUSD = roundUSD2(rawTotalCost)

	// 2. Group by model_id (sorted by cost_usd DESC)
	modelQuery := `
		SELECT
			COALESCE(NULLIF(selected_model_id, ''), COALESCE(requested_model, 'unknown')) AS model_id,
			SUM(CASE WHEN cost_usd IS NOT NULL THEN cost_usd ELSE 0 END) AS sum_cost,
			COUNT(cost_usd) AS known_cost_count,
			COALESCE(SUM(COALESCE(billed_tokens, 0)), 0) AS sum_tokens,
			COUNT(*) AS req_count
		FROM ` + tableName + `
		WHERE timestamp >= ?
		GROUP BY model_id
	`
	mRows, err := db.Query(modelQuery, cutoffISO)
	if err != nil {
		return resp, err
	}
	defer mRows.Close()

	for mRows.Next() {
		var modelID string
		var sumCost float64
		var knownCount int64
		var tokens, reqs int64
		if err := mRows.Scan(&modelID, &sumCost, &knownCount, &tokens, &reqs); err != nil {
			return resp, err
		}
		pInfo, hasPrice := LookupModelPrice(modelID)
		item := CostModelBreakdown{
			ModelID:   modelID,
			Tokens:    tokens,
			Requests:  reqs,
			Estimated: pInfo.Estimated,
		}
		if knownCount == 0 && !hasPrice {
			item.PriceUnknown = true
			item.CostUSD = nil
		} else {
			rounded := roundUSD2(sumCost)
			item.CostUSD = &rounded
		}
		resp.ByModel = append(resp.ByModel, item)
	}

	sort.Slice(resp.ByModel, func(i, j int) bool {
		if resp.ByModel[i].PriceUnknown != resp.ByModel[j].PriceUnknown {
			return !resp.ByModel[i].PriceUnknown
		}
		ci := 0.0
		if resp.ByModel[i].CostUSD != nil {
			ci = *resp.ByModel[i].CostUSD
		}
		cj := 0.0
		if resp.ByModel[j].CostUSD != nil {
			cj = *resp.ByModel[j].CostUSD
		}
		if ci != cj {
			return ci > cj
		}
		return resp.ByModel[i].Tokens > resp.ByModel[j].Tokens
	})

	// 3. Group by CST day (datetime(timestamp, '+8 hours'))
	dayQuery := `
		SELECT
			strftime('%Y-%m-%d', datetime(timestamp, '+8 hours')) AS cst_date,
			COALESCE(SUM(CASE WHEN cost_usd IS NOT NULL THEN cost_usd ELSE 0 END), 0) AS sum_cost,
			COALESCE(SUM(COALESCE(billed_tokens, 0)), 0) AS sum_tokens,
			COUNT(*) AS req_count
		FROM ` + tableName + `
		WHERE timestamp >= ?
		GROUP BY cst_date
		ORDER BY cst_date ASC
	`
	dRows, err := db.Query(dayQuery, cutoffISO)
	if err != nil {
		return resp, err
	}
	defer dRows.Close()

	for dRows.Next() {
		var d CostDayBreakdown
		var rawCost float64
		if err := dRows.Scan(&d.Date, &rawCost, &d.Tokens, &d.Requests); err != nil {
			return resp, err
		}
		d.CostUSD = roundUSD2(rawCost)
		resp.ByDay = append(resp.ByDay, d)
	}

	// 4. Group by client_token_name
	clientQuery := `
		SELECT
			COALESCE(NULLIF(client_token_name, ''), '默认客户端令牌') AS client_name,
			COALESCE(SUM(CASE WHEN cost_usd IS NOT NULL THEN cost_usd ELSE 0 END), 0) AS sum_cost,
			COALESCE(SUM(COALESCE(billed_tokens, 0)), 0) AS sum_tokens,
			COUNT(*) AS req_count
		FROM ` + tableName + `
		WHERE timestamp >= ?
		GROUP BY client_name
		ORDER BY sum_cost DESC, sum_tokens DESC
	`
	cRows, err := db.Query(clientQuery, cutoffISO)
	if err != nil {
		return resp, err
	}
	defer cRows.Close()

	for cRows.Next() {
		var c CostClientBreakdown
		var rawCost float64
		if err := cRows.Scan(&c.ClientTokenName, &rawCost, &c.Tokens, &c.Requests); err != nil {
			return resp, err
		}
		c.CostUSD = roundUSD2(rawCost)
		resp.ByClient = append(resp.ByClient, c)
	}

	return resp, nil
}
