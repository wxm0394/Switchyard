package gateway

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// costHistoryDBPath is the history DB the cost summary reads (read-only).
// Production default is /opt/switchyard-history/audit_history.db; tests
// override it with a temp file.
var costHistoryDBPath = DefaultHistoryDBPath

// handleCostSummary serves GET /api/gateway/cost/summary?range=today|7d|30d.
// It reads the history DB read-only and returns market-reference costs
// (never actual spend). If the history DB is missing or lacks the cost_usd
// column, it returns 503 with a plain reason instead of a 500 stack.
func (g *Gateway) handleCostSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	db, err := sql.Open("sqlite", "file:"+costHistoryDBPath+"?mode=ro")
	if err != nil {
		writeJSON(w, 503, map[string]interface{}{"error": "cost summary unavailable: cannot open history DB: " + err.Error()})
		return
	}
	defer db.Close()
	resp, err := QueryCostSummary(db, "audit_history", r.URL.Query().Get("range"), time.Now().UTC())
	if err != nil {
		writeJSON(w, 503, map[string]interface{}{"error": "cost summary unavailable: " + err.Error()})
		return
	}
	writeJSON(w, 200, resp)
}

// parseUsageSplit extracts prompt/completion token counts from an upstream
// response body. Non-streaming bodies are plain JSON; streaming bodies are
// SSE (usage arrives in a data: chunk, only when the client asked for
// stream_options.include_usage). Returns ok=false when no usage split is
// present (no usage block, or only a total with no split).
func parseUsageSplit(body []byte, stream bool) (prompt, completion int, ok bool) {
	if stream {
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var chunk struct {
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}
			if chunk.Usage != nil {
				if p, c := chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens; p > 0 || c > 0 {
					prompt, completion, ok = p, c, true
				}
			}
		}
		return prompt, completion, ok
	}
	var parsed struct {
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.Usage == nil {
		return 0, 0, false
	}
	p, c := parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens
	if p <= 0 && c <= 0 {
		return 0, 0, false
	}
	return p, c, true
}
