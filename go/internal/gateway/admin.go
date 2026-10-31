package gateway

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
	"github.com/wxm0394/Switchyard/internal/crypto"
	"github.com/wxm0394/Switchyard/internal/upstream"
)

// RegisterAdmin mounts all /api/gateway/* routes. The caller must
// supply an AdminGuardConfig built from env; the guard is applied
// here, before any handler, exactly as Node's middleware does.
func (g *Gateway) RegisterAdmin(mux *http.ServeMux, guard AdminGuardConfig) {
	admin := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if ok, status, body := DecideAdminAccess(r, guard); !ok {
				writeJSON(w, status, body)
				return
			}
			// Admin CORS: only listed origins get an allow header.
			if origin := r.Header.Get("Origin"); origin != "" {
				for _, a := range guard.AllowedOrigins {
					if a == origin {
						w.Header().Set("Access-Control-Allow-Origin", origin)
						w.Header().Set("Vary", "Origin")
						w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
						w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Admin-Password")
						break
					}
				}
			}
			if r.Method == http.MethodOptions {
				if isOriginAllowed(r, guard.AllowedOrigins) {
					w.WriteHeader(204)
				} else {
					w.WriteHeader(403)
				}
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/gateway/overview", admin(g.handleOverview))
	mux.HandleFunc("/api/gateway/logs", admin(g.handleLogs))
	mux.HandleFunc("/api/gateway/config", admin(g.handleConfig))
	mux.HandleFunc("/api/gateway/keys", admin(g.handleKeys))
	mux.HandleFunc("/api/gateway/keys/", admin(g.handleKeyByID))
	mux.HandleFunc("/api/gateway/routes", admin(g.handleRoutes))
	mux.HandleFunc("/api/gateway/routes/", admin(g.handleRouteByID))
	mux.HandleFunc("/api/gateway/tokens", admin(g.handleTokens))
	mux.HandleFunc("/api/gateway/tokens/", admin(g.handleTokenByID))
	mux.HandleFunc("/api/gateway/aliases", admin(g.handleAliases))
	mux.HandleFunc("/api/gateway/aliases/", admin(g.handleAliasByID))
	mux.HandleFunc("/api/gateway/diagnostics", admin(g.handleDiagnostics))
	mux.HandleFunc("/api/gateway/diagnostics/cleanup", admin(g.handleDiagnosticsCleanup))
	mux.HandleFunc("/api/gateway/kaggle/refresh-status", admin(g.handleKaggleStatus))
	mux.HandleFunc("/api/gateway/kaggle/refresh", admin(g.handleKaggleRefresh))
	mux.HandleFunc("/api/gateway/probe", admin(g.handleProbe))
	mux.HandleFunc("/api/gateway/verify-model-tools", admin(g.handleVerifyModelTools))
	mux.HandleFunc("/api/gateway/catalog-watch", admin(g.handleCatalogWatch))
	mux.HandleFunc("/api/gateway/catalog-watch/run", admin(g.handleCatalogWatchRun))
	mux.HandleFunc("/api/gateway/import-probed", admin(g.handleOnboard501))
	mux.HandleFunc("/api/gateway/onboard", admin(g.handleOnboard501))
}

func readBody(r *http.Request) map[string]interface{} {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]interface{}{}
	}
	return m
}

func safeKey(k scheduler.ProviderKey) map[string]interface{} {
	return map[string]interface{}{
		"id": k.ID, "name": k.Name, "providerType": k.ProviderType, "keyMask": k.KeyMask,
		"keyState": k.KeyState, "cooldownUntil": nullOr(k.CooldownUntil), "consecutive429": k.Consecutive429,
		"accountNotes": k.AccountNotes, "appealUrl": k.AppealURL, "baseUrl": k.BaseURL, "createdAt": k.CreatedAt,
	}
}

func nullOr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (g *Gateway) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	logLimit := intQuery(r, "logLimit", 50, 1, 200)
	routes, keys, aliases, cfg, err := g.loadFleet()
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
		return
	}
	keyMap := map[string]scheduler.ProviderKey{}
	for _, k := range keys {
		keyMap[k.ID] = k
	}
	enabled := []scheduler.Route{}
	for _, rt := range routes {
		if rt.Enabled {
			enabled = append(enabled, rt)
		}
	}
	aliasOut := []interface{}{}
	for _, a := range aliases {
		ev := g.evaluatePool(a.TargetModels, enabled, keyMap)
		targetDetails := []interface{}{}
		for _, tm := range a.TargetModels {
			var found *scheduler.Route
			for i := range enabled {
				if enabled[i].ModelID == tm {
					found = &enabled[i]
					break
				}
			}
			if found == nil {
				targetDetails = append(targetDetails, map[string]interface{}{"modelId": tm, "upstreamModel": nil, "privateOnly": false, "exists": false, "effectiveState": "missing", "verificationStatus": "unconfigured", "reliabilityScore": nil, "successCount": 0, "failureCount": 0, "latencyP50": 0, "latencyP95": 0, "providerName": nil, "keyMask": nil, "keyState": nil, "dailyTokenLimit": nil, "billedTokensToday": 0, "quotaRemaining": nil, "quotaExhausted": false, "schedulerScore": nil, "schedulerBreakdown": nil})
				continue
			}
			k, _ := keyMap[found.ProviderKeyID]
			metrics, _ := g.Store.GetMetrics(found.ID)
			eff := g.effectiveStateWithHeal(*found, &k)
			ver := verificationStatus(*found, &k, metrics, eff)
			quota, _ := g.Store.GetQuota(k.ID)
			idx := 0
			for i, t := range a.TargetModels {
				if t == tm {
					idx = i
				}
			}
			score, bd := scheduler.ComputeAliasScore(idx, &found.ReliabilityScore, metrics.LatencyP50, &quota, ver, found.MaxContext, found.BaseURL, 10000, "neutral", nil, false)
			var quotaRemaining interface{}
			quotaExhausted := false
			if quota.DailyTokenLimit != nil {
				rem := *quota.DailyTokenLimit - quota.BilledTokensToday
				if rem < 0 {
					rem = 0
				}
				quotaRemaining = rem
				quotaExhausted = quota.BilledTokensToday >= *quota.DailyTokenLimit
			}
			targetDetails = append(targetDetails, map[string]interface{}{"modelId": tm, "upstreamModel": found.UpstreamModel, "privateOnly": found.PrivateOnly, "exists": true, "routeId": found.ID, "effectiveState": eff, "verificationStatus": ver, "reliabilityScore": found.ReliabilityScore, "successCount": metrics.SuccessCount, "failureCount": metrics.FailureCount, "latencyP50": metrics.LatencyP50, "latencyP95": metrics.LatencyP95, "providerName": k.Name, "keyMask": k.KeyMask, "keyState": k.KeyState, "dailyTokenLimit": quota.DailyTokenLimit, "billedTokensToday": quota.BilledTokensToday, "quotaRemaining": quotaRemaining, "quotaExhausted": quotaExhausted, "schedulerScore": score, "schedulerBreakdown": bd})
		}
		aliasOut = append(aliasOut, map[string]interface{}{"id": a.ID, "alias": a.Alias, "targetModels": a.TargetModels, "description": a.Description, "createdAt": a.CreatedAt, "poolStatus": ev.PoolStatus, "missingTargets": ev.Missing, "failedTargets": ev.Failed, "verifiedTargets": ev.Verified, "untestedTargets": ev.Untested, "isActive": len(ev.Verified)+len(ev.Untested) > 0, "callableCount": len(ev.Verified) + len(ev.Untested), "targetDetails": targetDetails, "nextTarget": nil, "nextTargetUpstreamModel": nil})
	}
	routesOut := []interface{}{}
	for _, rt := range routes {
		k, _ := keyMap[rt.ProviderKeyID]
		metrics, _ := g.Store.GetMetrics(rt.ID)
		eff := g.effectiveStateWithHeal(rt, &k)
		ver := verificationStatus(rt, &k, metrics, eff)
		tier := tierForRoute(rt)
		routesOut = append(routesOut, map[string]interface{}{"id": rt.ID, "name": rt.Name, "providerKeyId": rt.ProviderKeyID, "modelId": rt.ModelID, "displayName": rt.DisplayName, "baseUrl": rt.BaseURL, "providerType": rt.ProviderType, "upstreamModel": rt.UpstreamModel, "taskRole": rt.TaskRole, "routeState": rt.RouteState, "circuitState": rt.CircuitState, "circuitOpensUntil": nullOr(rt.CircuitOpensUntil), "consecutiveTimeouts": rt.ConsecutiveTimeouts, "consecutive5xx": rt.Consecutive5xx, "consecutiveFailures": rt.ConsecutiveFailures, "circuitOpenCount": rt.CircuitOpenCount, "supportsVision": rt.SupportsVision, "supportsTools": rt.SupportsTools, "supportsJsonMode": rt.SupportsJSONMode, "supportsStreaming": rt.SupportsStreaming, "maxContext": rt.MaxContext, "allowSensitive": rt.AllowSensitive, "privateOnly": rt.PrivateOnly, "privacyTier": rt.PrivacyTier, "costWeight": rt.CostWeight, "reliabilityScore": rt.ReliabilityScore, "enabled": rt.Enabled, "createdAt": rt.CreatedAt, "effectiveState": eff, "verificationStatus": ver, "metrics": metrics, "keyMask": keyMaskOr(k, "未绑定Key"), "providerName": providerNameOr(k, "Unknown Provider"), "isColdStart": isColdStartRoute(rt), "timeoutTier": tier})
	}
	keysOut := []interface{}{}
	for _, k := range keys {
		quota, _ := g.Store.GetQuota(k.ID)
		m := safeKey(k)
		m["counters"] = quota
		keysOut = append(keysOut, m)
	}
	tokens, _ := g.Store.ListTokens()
	tokenOut := []interface{}{}
	totalReq, totalTok, activeCount := 0, 0, 0
	for _, t := range tokens {
		tokenOut = append(tokenOut, tokenJSON(t))
		totalReq += t.RequestCount
		totalTok += t.UsedTokens
		if t.Active {
			activeCount++
		}
	}
	logs, _ := g.Store.ListAudit(logLimit, 0)
	avg := 0
	if len(logs) > 0 {
		sum := 0
		for _, l := range logs {
			sum += l.LatencyMs
		}
		avg = sum / len(logs)
	}
	writeJSON(w, 200, map[string]interface{}{
		"config": cfg, "providerKeys": keysOut, "catalogWatch": map[string]interface{}{"status": "not_implemented_in_go_phase3"},
		"routes": routesOut, "tokens": tokenOut, "aliases": aliasOut, "logs": logs,
		"stats": map[string]interface{}{"totalRequests": totalReq, "totalTokens": totalTok, "avgLatencyMs": avg, "activeTokensCount": activeCount, "rssMb": 0, "agentPool": map[string]interface{}{}},
	})
}

func keyMaskOr(k scheduler.ProviderKey, def string) string {
	if k.KeyMask == "" {
		return def
	}
	return k.KeyMask
}
func providerNameOr(k scheduler.ProviderKey, def string) string {
	if k.Name == "" {
		return def
	}
	return k.Name
}
func tierForRoute(rt scheduler.Route) map[string]interface{} {
	tier := "reasoning"
	timeout := 60000
	if isColdStartRoute(rt) {
		return map[string]interface{}{"timeoutMs": 120000, "isColdStart": true, "tier": "cold_start"}
	}
	if rt.TaskRole == "fast" {
		tier = "fast"
	}
	return map[string]interface{}{"timeoutMs": timeout, "isColdStart": false, "tier": tier}
}
func isColdStartRoute(rt scheduler.Route) bool {
	return upstream.IsColdStart(rt.ID, rt.Name, rt.ModelID, rt.UpstreamModel, rt.BaseURL)
}

func tokenJSON(t store.ClientToken) map[string]interface{} {
	return map[string]interface{}{"id": t.ID, "name": t.Name, "token": t.Token, "createdAt": t.CreatedAt, "lastUsedAt": nullOr(t.LastUsedAt), "active": t.Active, "quotaLimit": t.QuotaLimit, "usedTokens": t.UsedTokens, "requestCount": t.RequestCount, "note": t.Note}
}

func (g *Gateway) handleLogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit := intQuery(r, "limit", 50, 1, 200)
		offset := intQuery(r, "offset", 0, 0, 1<<30)
		logs, err := g.Store.ListAudit(limit, offset)
		if err != nil {
			writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]interface{}{"logs": logs, "limit": limit, "offset": offset})
	case http.MethodDelete:
		// Node clearAuditLogs: MemStore clears by dropping audit; SQLite
		// implementation deletes all rows via a dedicated path below.
		// This handler uses the store interface; if the store does not
		// support clear, it is a documented gap (admin-only, low use).
		writeJSON(w, 501, map[string]interface{}{"error": "clear audit logs is not implemented in the Go backend yet"})
	default:
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
	}
}

func (g *Gateway) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	patch := readBody(r)
	if v, ok := patch["privacyMode"].(string); ok && v != "off" && v != "low" && v != "high" {
		writeJSON(w, 400, map[string]interface{}{"error": "privacyMode must be off, low, or high"})
		return
	}
	if v, ok := patch["dispatchMode"].(string); ok && v != "auto" && v != "pinned" {
		writeJSON(w, 400, map[string]interface{}{"error": "dispatchMode must be auto or pinned"})
		return
	}
	cfg, err := g.Store.UpdateConfig(patch)
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, 200, cfg)
}

func (g *Gateway) handleKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	body := readBody(r)
	name, _ := body["name"].(string)
	if strings.TrimSpace(name) == "" {
		writeJSON(w, 400, map[string]interface{}{"error": "Key name is required"})
		return
	}
	// Key creation requires the master key; without it, fail loudly.
	if g.Cfg.MasterKey == nil {
		writeJSON(w, 503, map[string]interface{}{"error": "master key is not configured; cannot encrypt a new provider key"})
		return
	}
	rawKey, _ := body["rawApiKey"].(string)
	enc := ""
	if strings.TrimSpace(rawKey) != "" {
		// Encrypt via the crypto package with the configured key.
		// The gateway's Decrypt injector is for reads; encryption uses
		// the same master key bytes.
		var err error
		enc, err = encryptWithMaster(g.Cfg.MasterKey, strings.TrimSpace(rawKey))
		if err != nil {
			writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
			return
		}
	}
	id, _ := body["id"].(string)
	if id == "" {
		id = "pkey-" + newRequestID()
	}
	pt, _ := body["providerType"].(string)
	if pt == "" {
		pt = "openai-compatible"
	}
	k := scheduler.ProviderKey{ID: id, Name: strings.TrimSpace(name), ProviderType: pt, EncryptedKey: enc, KeyMask: cryptoMask(strings.TrimSpace(rawKey)), KeyState: "healthy", BaseURL: stringVal(body["baseUrl"])}
	if strings.TrimSpace(rawKey) == "" {
		k.KeyState = "unconfigured"
	}
	if err := g.Store.UpsertKey(k); err != nil {
		writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
		return
	}
	quota, _ := g.Store.GetQuota(k.ID)
	out := safeKey(k)
	out["counters"] = quota
	writeJSON(w, 201, out)
}

func (g *Gateway) handleKeyByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/gateway/keys/")
	if strings.HasSuffix(id, "/reset") && r.Method == http.MethodPost {
		realID := strings.TrimSuffix(id, "/reset")
		_ = g.Store.UpdateKeyState(realID, "healthy", "", false)
		writeJSON(w, 200, map[string]interface{}{"ok": true, "message": "Provider key reset to healthy state"})
		return
	}
	switch r.Method {
	case http.MethodPatch:
		body := readBody(r)
		keys, _ := g.Store.ListKeys()
		var found *scheduler.ProviderKey
		for i := range keys {
			if keys[i].ID == id {
				found = &keys[i]
				break
			}
		}
		if found == nil {
			writeJSON(w, 404, map[string]interface{}{"error": "Provider key not found"})
			return
		}
		if v, ok := body["name"].(string); ok {
			found.Name = v
		}
		if v, ok := body["providerType"].(string); ok {
			found.ProviderType = v
		}
		if v, ok := body["baseUrl"].(string); ok {
			found.BaseURL = v
		}
		if v, ok := body["rawApiKey"].(string); ok && strings.TrimSpace(v) != "" {
			if g.Cfg.MasterKey == nil {
				writeJSON(w, 503, map[string]interface{}{"error": "master key is not configured"})
				return
			}
			enc, err := encryptWithMaster(g.Cfg.MasterKey, strings.TrimSpace(v))
			if err != nil {
				writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
				return
			}
			found.EncryptedKey = enc
			found.KeyMask = cryptoMask(strings.TrimSpace(v))
			found.KeyState = "healthy"
		}
		if err := g.Store.UpsertKey(*found); err != nil {
			writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
			return
		}
		quota, _ := g.Store.GetQuota(found.ID)
		out := safeKey(*found)
		out["counters"] = quota
		writeJSON(w, 200, out)
	case http.MethodDelete:
		_ = g.Store.DeleteKey(id)
		writeJSON(w, 200, map[string]interface{}{"ok": true})
	default:
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
	}
}

func (g *Gateway) handleRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	body := readBody(r)
	modelID, _ := body["modelId"].(string)
	if strings.TrimSpace(modelID) == "" {
		writeJSON(w, 400, map[string]interface{}{"error": "modelId is required"})
		return
	}
	providerKeyID, _ := body["providerKeyId"].(string)
	if rawKey, ok := body["rawApiKey"].(string); ok && strings.TrimSpace(rawKey) != "" {
		// Create a key (requires master key) and use it.
		if g.Cfg.MasterKey == nil {
			writeJSON(w, 503, map[string]interface{}{"error": "master key is not configured"})
			return
		}
		enc, err := encryptWithMaster(g.Cfg.MasterKey, strings.TrimSpace(rawKey))
		if err != nil {
			writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
			return
		}
		provider, _ := body["provider"].(string)
		if provider == "" {
			provider = "Provider"
		}
		pt, _ := body["providerType"].(string)
		if pt == "" {
			pt = "openai-compatible"
		}
		nk := scheduler.ProviderKey{ID: "pkey-" + newRequestID(), Name: provider + " Key (" + modelID + ")", ProviderType: pt, EncryptedKey: enc, KeyMask: cryptoMask(strings.TrimSpace(rawKey)), KeyState: "healthy", BaseURL: stringVal(body["baseUrl"])}
		_ = g.Store.UpsertKey(nk)
		providerKeyID = nk.ID
	} else if providerKeyID == "" {
		keys, _ := g.Store.ListKeys()
		if len(keys) > 0 {
			providerKeyID = keys[0].ID
		}
	}
	baseURL, _ := body["baseUrl"].(string)
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" && providerKeyID != "" {
		keys, _ := g.Store.ListKeys()
		for _, k := range keys {
			if k.ID == providerKeyID {
				baseURL = k.BaseURL
			}
		}
	}
	if baseURL == "" {
		writeJSON(w, 400, map[string]interface{}{"error": "该供应商在第三层还没有填上游接口 URL，请先在第三层编辑该供应商填好 URL，再回第二层选它建路由。"})
		return
	}
	route := routeFromBody(body, providerKeyID, baseURL)
	if err := g.Store.UpsertRoute(route); err != nil {
		writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, 201, route)
}

func routeFromBody(body map[string]interface{}, providerKeyID, baseURL string) scheduler.Route {
	boolVal := func(name string, def bool) bool {
		if v, ok := body[name].(bool); ok {
			return v
		}
		return def
	}
	intVal := func(name string, def int) int {
		if v, ok := body[name].(float64); ok {
			return int(v)
		}
		return def
	}
	strVal := func(name, def string) string {
		if v, ok := body[name].(string); ok && v != "" {
			return v
		}
		return def
	}
	id, _ := body["id"].(string)
	if id == "" {
		id = "route-" + newRequestID()
	}
	return scheduler.Route{
		ID: id, Name: strVal("name", modelIDOr(body)), ProviderKeyID: providerKeyID,
		ModelID: strVal("modelId", ""), DisplayName: strVal("displayName", strVal("modelId", "")),
		BaseURL: baseURL, ProviderType: strVal("providerType", "openai-compatible"),
		UpstreamModel: strVal("upstreamModel", strVal("modelId", "")), TaskRole: strVal("taskRole", "auto"),
		RouteState: "healthy", CircuitState: "closed",
		SupportsVision: boolVal("supportsVision", false), SupportsTools: boolVal("supportsTools", true),
		SupportsJSONMode: boolVal("supportsJsonMode", true), SupportsStreaming: boolVal("supportsStreaming", true),
		MaxContext: intVal("maxContext", 128000), AllowSensitive: boolVal("allowSensitive", false),
		PrivateOnly: boolVal("privateOnly", false), PrivacyTier: strVal("privacyTier", "cloud_zero_retention"),
		CostWeight: intVal("costWeight", 1), ReliabilityScore: intVal("reliabilityScore", 100),
		Enabled: boolVal("enabled", true),
	}
}

func modelIDOr(body map[string]interface{}) string {
	if v, ok := body["modelId"].(string); ok {
		return v
	}
	return ""
}

func (g *Gateway) handleRouteByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/gateway/routes/")
	if strings.HasSuffix(rest, "/reset") && r.Method == http.MethodPost {
		id := strings.TrimSuffix(rest, "/reset")
		_ = g.Store.UpdateRouteState(id, "healthy", "")
		_ = g.Store.UpdateCircuit(id, "closed", "", 0, 0)
		writeJSON(w, 200, map[string]interface{}{"ok": true, "message": "Model route circuit breaker & status reset to healthy"})
		return
	}
	if strings.HasSuffix(rest, "/verify-tools") && r.Method == http.MethodPost {
		writeJSON(w, 501, map[string]interface{}{"error": "route verify-tools is not implemented in the Go backend yet"})
		return
	}
	id := rest
	switch r.Method {
	case http.MethodPatch:
		body := readBody(r)
		routes, _ := g.Store.ListRoutes()
		var found *scheduler.Route
		for i := range routes {
			if routes[i].ID == id {
				found = &routes[i]
				break
			}
		}
		if found == nil {
			writeJSON(w, 404, map[string]interface{}{"error": "Model route not found"})
			return
		}
		// Partial patch: only fields present in the body are changed.
		if v, ok := body["displayName"].(string); ok {
			found.DisplayName = v
		}
		if v, ok := body["baseUrl"].(string); ok {
			found.BaseURL = v
		}
		if v, ok := body["enabled"].(bool); ok {
			found.Enabled = v
		}
		if v, ok := body["allowSensitive"].(bool); ok {
			found.AllowSensitive = v
		}
		if v, ok := body["privateOnly"].(bool); ok {
			found.PrivateOnly = v
		}
		if v, ok := body["maxContext"].(float64); ok {
			found.MaxContext = int(v)
		}
		if err := g.Store.UpsertRoute(*found); err != nil {
			writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
			return
		}
		writeJSON(w, 200, found)
	case http.MethodDelete:
		_ = g.Store.DeleteRoute(id)
		writeJSON(w, 200, map[string]interface{}{"ok": true})
	default:
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
	}
}

func (g *Gateway) handleTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	body := readBody(r)
	name, _ := body["name"].(string)
	if strings.TrimSpace(name) == "" {
		writeJSON(w, 400, map[string]interface{}{"error": "Token name is required"})
		return
	}
	var ql *int
	if v, ok := body["quotaLimit"].(float64); ok {
		x := int(v)
		ql = &x
	}
	t := store.ClientToken{ID: "tok-" + newRequestID(), Name: strings.TrimSpace(name), Token: "sk-portal-" + newRequestID(), CreatedAt: time.Now().Format(time.RFC3339), Active: true, QuotaLimit: ql, Note: stringVal(body["note"])}
	_ = g.Store.UpsertToken(t)
	writeJSON(w, 201, tokenJSON(t))
}

func (g *Gateway) handleTokenByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/gateway/tokens/")
	switch r.Method {
	case http.MethodPatch:
		body := readBody(r)
		tokens, _ := g.Store.ListTokens()
		var found *store.ClientToken
		for i := range tokens {
			if tokens[i].ID == id {
				found = &tokens[i]
				break
			}
		}
		if found == nil {
			// Node upsert creates when missing; Go keeps the same
			// upsert behaviour for PATCH by id.
			found = &store.ClientToken{ID: id, CreatedAt: time.Now().Format(time.RFC3339), Active: true}
		}
		if v, ok := body["name"].(string); ok {
			found.Name = strings.TrimSpace(v)
		}
		if v, ok := body["active"].(bool); ok {
			found.Active = v
		}
		if v, ok := body["note"].(string); ok {
			found.Note = strings.TrimSpace(v)
		}
		if q, ok := body["quotaLimit"]; ok {
			if q == nil || q == "" {
				found.QuotaLimit = nil
			} else if f, ok := q.(float64); ok {
				x := int(f)
				found.QuotaLimit = &x
			}
		}
		_ = g.Store.UpsertToken(*found)
		writeJSON(w, 200, tokenJSON(*found))
	case http.MethodDelete:
		_ = g.Store.DeleteToken(id)
		writeJSON(w, 200, map[string]interface{}{"ok": true})
	default:
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
	}
}

func (g *Gateway) handleAliases(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		aliases, _ := g.Store.ListAliases()
		routes, _ := g.Store.ListRoutes()
		keys, _ := g.Store.ListKeys()
		keyMap := map[string]scheduler.ProviderKey{}
		for _, k := range keys {
			keyMap[k.ID] = k
		}
		out := []interface{}{}
		for _, a := range aliases {
			ev := g.evaluatePool(a.TargetModels, routes, keyMap)
			out = append(out, map[string]interface{}{"id": a.ID, "alias": a.Alias, "targetModels": a.TargetModels, "description": a.Description, "createdAt": a.CreatedAt, "poolStatus": ev.PoolStatus, "missingTargets": ev.Missing, "failedTargets": ev.Failed, "verifiedTargets": ev.Verified, "untestedTargets": ev.Untested})
		}
		writeJSON(w, 200, out)
	case http.MethodPost:
		body := readBody(r)
		alias, _ := body["alias"].(string)
		if strings.TrimSpace(alias) == "" {
			writeJSON(w, 400, map[string]interface{}{"error": "alias 字段不能为空"})
			return
		}
		tm, ok := body["targetModels"].([]interface{})
		if !ok || len(tm) == 0 {
			writeJSON(w, 400, map[string]interface{}{"error": "targetModels 必须为非空模型名称数组"})
			return
		}
		targets := []string{}
		for _, t := range tm {
			if s, ok := t.(string); ok && strings.TrimSpace(s) != "" {
				targets = append(targets, strings.TrimSpace(s))
			}
		}
		id, _ := body["id"].(string)
		if id == "" {
			id = "alias-" + newRequestID()
		}
		a := store.Alias{ID: id, Alias: strings.TrimSpace(alias), TargetModels: targets, Description: stringVal(body["description"]), CreatedAt: time.Now().Format(time.RFC3339)}
		_ = g.Store.UpsertAlias(a)
		writeJSON(w, 200, map[string]interface{}{"id": a.ID, "alias": a.Alias, "targetModels": a.TargetModels, "description": a.Description, "createdAt": a.CreatedAt})
	default:
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
	}
}

func (g *Gateway) handleAliasByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/gateway/aliases/")
	switch r.Method {
	case http.MethodPatch:
		body := readBody(r)
		if tm, ok := body["targetModels"]; ok {
			arr, ok := tm.([]interface{})
			if !ok || len(arr) == 0 {
				writeJSON(w, 400, map[string]interface{}{"error": "targetModels 必须为非空模型名称数组"})
				return
			}
		}
		aliases, _ := g.Store.ListAliases()
		var found *store.Alias
		for i := range aliases {
			if aliases[i].ID == id {
				found = &aliases[i]
				break
			}
		}
		if found == nil {
			writeJSON(w, 404, map[string]interface{}{"error": "Model alias not found"})
			return
		}
		if v, ok := body["alias"].(string); ok {
			found.Alias = v
		}
		if v, ok := body["description"].(string); ok {
			found.Description = v
		}
		if tm, ok := body["targetModels"].([]interface{}); ok {
			targets := []string{}
			for _, t := range tm {
				if s, ok := t.(string); ok {
					targets = append(targets, s)
				}
			}
			found.TargetModels = targets
		}
		_ = g.Store.UpsertAlias(*found)
		writeJSON(w, 200, map[string]interface{}{"id": found.ID, "alias": found.Alias, "targetModels": found.TargetModels, "description": found.Description, "createdAt": found.CreatedAt})
	case http.MethodDelete:
		aliases, _ := g.Store.ListAliases()
		ok := false
		for _, a := range aliases {
			if a.ID == id {
				ok = true
			}
		}
		_ = g.Store.DeleteAlias(id)
		writeJSON(w, 200, map[string]interface{}{"success": ok})
	default:
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
	}
}

func (g *Gateway) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	routes, _ := g.Store.ListRoutes()
	keys, _ := g.Store.ListKeys()
	aliases, _ := g.Store.ListAliases()
	writeJSON(w, 200, map[string]interface{}{"routeCount": len(routes), "keyCount": len(keys), "aliasCount": len(aliases), "orphanedRoutes": []interface{}{}, "staleAliases": []interface{}{}, "cleanupChecklist": []interface{}{}, "note": "Full Node diagnostics checklist wording is a later PR; counts above are real."})
}

func (g *Gateway) handleDiagnosticsCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	body := readBody(r)
	deletedRoutes, deletedKeys := 0, 0
	if arr, ok := body["deleteRouteIds"].([]interface{}); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				_ = g.Store.DeleteRoute(s)
				deletedRoutes++
			}
		}
	}
	if arr, ok := body["deleteKeyIds"].([]interface{}); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				_ = g.Store.DeleteKey(s)
				deletedKeys++
			}
		}
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "deletedRoutes": deletedRoutes, "deletedKeys": deletedKeys})
}

// handleKaggleStatus returns Node's getKaggleRefreshStatus shape:
// enabled, providerName, modelId, providerFound, keyState, expiresAt,
// remainingSeconds, lastRefreshAt, lastError, consecutiveFailures.
// Nulls (not empty strings) are used where Node returns null: no
// provider found, or no refresh state row yet.
func (g *Gateway) handleKaggleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	if g.Kaggle == nil {
		writeJSON(w, 200, map[string]interface{}{"enabled": false, "note": "Kaggle timer is not configured in this gateway instance."})
		return
	}
	st, err := g.Kaggle.Status()
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
		return
	}
	out := map[string]interface{}{
		"enabled": st.Enabled, "providerName": st.ProviderName, "modelId": st.ModelID,
		"providerFound": st.ProviderFound, "consecutiveFailures": st.ConsecutiveFailures,
	}
	if st.ProviderFound {
		out["keyState"] = st.KeyState
	} else {
		out["keyState"] = nil
	}
	if st.ExpiresAt != "" {
		out["expiresAt"] = st.ExpiresAt
	} else {
		out["expiresAt"] = nil
	}
	if st.RemainingSeconds != nil {
		out["remainingSeconds"] = *st.RemainingSeconds
	} else {
		out["remainingSeconds"] = nil
	}
	if st.LastRefreshAt != "" {
		out["lastRefreshAt"] = st.LastRefreshAt
	} else {
		out["lastRefreshAt"] = nil
	}
	if st.LastError != "" {
		out["lastError"] = st.LastError
	} else {
		out["lastError"] = nil
	}
	writeJSON(w, 200, out)
}

// handleKaggleRefresh runs one Kaggle refresh, exactly as Node's
// POST /api/gateway/kaggle/refresh does, and returns {action, reason}.
// When the timer is not configured for this instance it returns 501
// with an honest message, not a fake success.
func (g *Gateway) handleKaggleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	if g.Kaggle == nil {
		writeJSON(w, 501, map[string]interface{}{"error": "Kaggle timer is not configured in this gateway instance"})
		return
	}
	result, err := g.Kaggle.RunOnce(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"action": result.Action, "reason": result.Reason})
}

func (g *Gateway) handleCatalogWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"status": "not_implemented_in_go_phase3"})
}

func (g *Gateway) handleCatalogWatchRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	writeJSON(w, 501, map[string]interface{}{"error": "Catalog watch run is not implemented in the Go backend yet"})
}

func (g *Gateway) handleVerifyModelTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	body := readBody(r)
	baseURL, _ := body["baseUrl"].(string)
	modelID, _ := body["modelId"].(string)
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(modelID) == "" {
		writeJSON(w, 400, map[string]interface{}{"supported": false, "latencyMs": 0, "error": "baseUrl and modelId are required"})
		return
	}
	writeJSON(w, 501, map[string]interface{}{"supported": false, "latencyMs": 0, "error": "verify-model-tools is not implemented in the Go backend yet"})
}

func (g *Gateway) handleOnboard501(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	writeJSON(w, 501, map[string]interface{}{"error": "Onboard batch import is not implemented in the Go backend yet"})
}

// SSRF guard for probe: refuse private, loopback, link-local,
// reserved, and metadata addresses before any fetch.
func isSSRFBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if ip.String() == "169.254.169.254" {
		return true
	}
	// Reserved / documentation ranges that must never be probed.
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
		_, block, err := net.ParseCIDR(cidr)
		if err == nil && block.Contains(ip) {
			return true
		}
	}
	return false
}

func (g *Gateway) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"error": "method not allowed"})
		return
	}
	body := readBody(r)
	baseURL, _ := body["baseUrl"].(string)
	baseURL = strings.TrimSpace(baseURL)
	providerKeyID, _ := body["providerKeyId"].(string)
	var storedKey *scheduler.ProviderKey
	if providerKeyID != "" {
		keys, _ := g.Store.ListKeys()
		for i := range keys {
			if keys[i].ID == providerKeyID {
				storedKey = &keys[i]
				break
			}
		}
		if storedKey == nil {
			writeJSON(w, 404, map[string]interface{}{"success": false, "error": "providerKeyId not found"})
			return
		}
		if baseURL == "" {
			baseURL = storedKey.BaseURL
		}
	}
	if baseURL == "" {
		writeJSON(w, 400, map[string]interface{}{"success": false, "error": "没有上游接口 URL：请先在第三层给该供应商填好 URL，再回第二层探测。"})
		return
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		writeJSON(w, 400, map[string]interface{}{"success": false, "error": "invalid baseUrl"})
		return
	}
	host := u.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		writeJSON(w, 400, map[string]interface{}{"success": false, "error": "could not resolve upstream host"})
		return
	}
	for _, ip := range ips {
		if isSSRFBlockedIP(ip) {
			writeJSON(w, 400, map[string]interface{}{"success": false, "error": "upstream host resolves to a private or reserved address; probe refused (SSRF guard)"})
			return
		}
	}
	apiKey, _ := body["apiKey"].(string)
	if strings.TrimSpace(apiKey) == "" && storedKey != nil {
		dec, err := g.decrypt(storedKey.EncryptedKey)
		if err != nil {
			writeJSON(w, 400, map[string]interface{}{"success": false, "error": "stored key could not be decrypted"})
			return
		}
		apiKey = dec
	}
	modelsURL := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errText := string(b)
		if resp.StatusCode == 401 {
			errText += "（上游说 Key 无效：先确认这把 Key 是不是这个供应商的——Groq 是 gsk_ 开头，OpenRouter 是 sk-or- 开头，Kaggle 是 kagg…，不能混用；没选 Key 时也会报这个错）"
		}
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": errText})
		return
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(b, &parsed)
	models := []string{}
	for _, m := range parsed.Data {
		models = append(models, m.ID)
	}
	writeJSON(w, 200, map[string]interface{}{"success": true, "models": models})
}

func encryptWithMaster(key []byte, plaintext string) (string, error) {
	return crypto.Encrypt(key, plaintext)
}

func cryptoMask(raw string) string {
	return crypto.Mask(raw)
}

