// Package gateway implements the core gateway: POST
// /v1/chat/completions, GET /v1/models, and the admin API surface
// documented in GO_API_CONTRACT.md. Phase 5 adds tool-rescue,
// tool-args, the Kaggle timer, privacy routing, and auto-enroll.
// Gemini conversion is NOT in this pass (see GO_API_CONTRACT.md §7).
package gateway

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wxm0394/Switchyard/internal/kaggle"
	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
	"github.com/wxm0394/Switchyard/internal/toolargs"
	"github.com/wxm0394/Switchyard/internal/toolrescue"
	"github.com/wxm0394/Switchyard/internal/upstream"
	"github.com/wxm0394/Switchyard/internal/validator"
)

type Config struct {
	MasterKey []byte
	// Decrypt is injected so tests can use a test key without env.
	Decrypt func(encrypted string) (string, error)
}

type Gateway struct {
	Store    store.Store
	Cfg      Config
	Rotator  *scheduler.Rotator
	// Kaggle is the Phase 5 Kaggle timer refresher. Nil means the
	// timer is not configured for this instance; the admin Kaggle
	// endpoints then report honestly instead of faking a result.
	Kaggle   *kaggle.Refresher
	mu       sync.Mutex
	rateWin  map[string]*rateWindow
	halfOpen map[string]time.Time
}

type rateWindow struct {
	minute int64
	count  int
}

func New(s store.Store, cfg Config) *Gateway {
	return &Gateway{Store: s, Cfg: cfg, Rotator: scheduler.NewRotator(), rateWin: map[string]*rateWindow{}, halfOpen: map[string]time.Time{}}
}

func (g *Gateway) decrypt(enc string) (string, error) {
	if g.Cfg.Decrypt != nil {
		return g.Cfg.Decrypt(enc)
	}
	return "", fmt.Errorf("no decryptor configured")
}

// ---- admin guard (GO_API_CONTRACT.md §1) ----

type AdminGuardConfig struct {
	Password     string
	Production   bool
	AllowNoAuth  bool
	AllowedHosts []string
	AllowedOrigins []string
}

func isHostAllowed(r *http.Request, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	host := strings.TrimSpace(strings.ToLower(r.Host))
	if host == "" {
		return false
	}
	name := host
	if strings.HasPrefix(host, "[") {
		if idx := strings.Index(host, "]"); idx >= 0 {
			name = host[:idx+1]
		}
	} else if idx := strings.Index(host, ":"); idx >= 0 {
		name = host[:idx]
	}
	for _, a := range allowed {
		if a == host || a == name {
			return true
		}
	}
	return false
}

func isOriginAllowed(r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if origin == "null" {
		return false
	}
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	// Same-origin: origin host == request host.
	if u, err := parseOriginHost(origin); err == nil && u == r.Host {
		return true
	}
	// Origin host without port vs Host with port: compare bare names.
	return false
}

func parseOriginHost(origin string) (string, error) {
	// origin is scheme://host[:port]
	rest := origin
	if idx := strings.Index(rest, "://"); idx >= 0 {
		rest = rest[idx+3:]
	}
	if idx := strings.Index(rest, "/"); idx >= 0 {
		rest = rest[:idx]
	}
	if rest == "" {
		return "", fmt.Errorf("empty origin host")
	}
	return rest, nil
}

// DecideAdminAccess returns (ok, status, body).
func DecideAdminAccess(r *http.Request, cfg AdminGuardConfig) (bool, int, map[string]interface{}) {
	if !isHostAllowed(r, cfg.AllowedHosts) {
		return false, 403, map[string]interface{}{"error": "Host not allowed for the admin API", "code": "admin_host_forbidden"}
	}
	if !isOriginAllowed(r, cfg.AllowedOrigins) {
		return false, 403, map[string]interface{}{"error": "Cross-origin admin requests are not allowed", "code": "admin_origin_forbidden"}
	}
	if cfg.Password == "" {
		if cfg.Production && !cfg.AllowNoAuth {
			return false, 503, map[string]interface{}{"error": "Admin API is disabled: set GATEWAY_ADMIN_PASSWORD (or GATEWAY_ALLOW_NO_ADMIN_AUTH=true to run without one)", "code": "admin_password_not_configured"}
		}
		return true, 0, nil
	}
	provided := strings.TrimSpace(r.Header.Get("X-Admin-Password"))
	if provided == "" || !constantTimeEqual(provided, cfg.Password) {
		return false, 401, map[string]interface{}{"error": "Admin password required", "requiresAdminPassword": true}
	}
	return true, 0, nil
}

func constantTimeEqual(a, b string) bool {
	// Hash first so lengths never leak (Node safeEqual).
	ha := sha256Sum(a)
	hb := sha256Sum(b)
	if len(ha) != len(hb) {
		return false
	}
	diff := 0
	for i := range ha {
		diff |= int(ha[i] ^ hb[i])
	}
	return diff == 0
}

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func openAIError(w http.ResponseWriter, status int, message, typ, code string) {
	writeJSON(w, status, map[string]interface{}{"error": map[string]interface{}{"message": message, "type": typ, "code": code}})
}

func (g *Gateway) checkRateLimit(tokenID string, rpm int) bool {
	if rpm <= 0 {
		rpm = 120
	}
	nowMin := time.Now().Unix() / 60
	g.mu.Lock()
	defer g.mu.Unlock()
	win, ok := g.rateWin[tokenID]
	if !ok || win.minute != nowMin {
		g.rateWin[tokenID] = &rateWindow{minute: nowMin, count: 1}
		return true
	}
	if win.count >= rpm {
		return false
	}
	win.count++
	return true
}

func (g *Gateway) halfOpenLocked(routeID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.halfOpen[routeID]
	return ok && time.Now().Before(until)
}
func (g *Gateway) acquireHalfOpen(routeID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until, ok := g.halfOpen[routeID]; ok && time.Now().Before(until) {
		return false
	}
	g.halfOpen[routeID] = time.Now().Add(30 * time.Second)
	return true
}
func (g *Gateway) releaseHalfOpen(routeID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.halfOpen, routeID)
}

// loadFleet loads routes/keys/aliases/config and applies circuit
// auto-heal transitions that Node performs inside getEffectiveState
// (open -> half_open after opens_until; the store is updated so a
// later request sees half_open, not a stale open).
func (g *Gateway) loadFleet() (routes []scheduler.Route, keys map[string]scheduler.ProviderKey, aliases []store.Alias, cfg scheduler.SchedulerConfig, err error) {
	routes, err = g.Store.ListRoutes()
	if err != nil {
		return
	}
	kl, err := g.Store.ListKeys()
	if err != nil {
		return
	}
	keys = map[string]scheduler.ProviderKey{}
	for _, k := range kl {
		keys[k.ID] = k
	}
	aliases, err = g.Store.ListAliases()
	if err != nil {
		return
	}
	cfg, err = g.Store.GetConfig()
	return
}

// effectiveStateWithHeal mirrors Node getEffectiveState, including
// key cooldown auto-heal (cooldownUntil passed -> healthy) and
// circuit open -> half_open transition. It updates the store for
// transitions it performs.
func (g *Gateway) effectiveStateWithHeal(route scheduler.Route, key *scheduler.ProviderKey) string {
	if !route.Enabled || route.RouteState == "disabled" {
		return "disabled"
	}
	if key == nil {
		return "unconfigured"
	}
	if key.KeyState == "disabled" {
		return "disabled"
	}
	if key.KeyState == "unconfigured" || strings.TrimSpace(key.EncryptedKey) == "" {
		return "unconfigured"
	}
	if key.KeyState == "cooldown" {
		if key.CooldownUntil != "" {
			if t, err := time.Parse(time.RFC3339, key.CooldownUntil); err == nil && !time.Now().Before(t) {
				_ = g.Store.UpdateKeyState(key.ID, "healthy", "", false)
				key.KeyState = "healthy"
			} else {
				return "cooldown"
			}
		} else {
			return "cooldown"
		}
	}
	if key.KeyState == "exhausted" {
		return "exhausted"
	}
	if route.RouteState == "unavailable_for_key" {
		if route.UnavailableUntil != "" {
			if t, err := time.Parse(time.RFC3339, route.UnavailableUntil); err == nil && !time.Now().Before(t) {
				_ = g.Store.UpdateRouteState(route.ID, "healthy", "")
				route.RouteState = "healthy"
			} else {
				return "unavailable_for_key"
			}
		} else {
			return "unavailable_for_key"
		}
	}
	if route.CircuitState == "open" {
		if route.CircuitOpensUntil != "" {
			if t, err := time.Parse(time.RFC3339, route.CircuitOpensUntil); err == nil && !time.Now().Before(t) {
				_ = g.Store.UpdateCircuit(route.ID, "half_open", "", route.ConsecutiveTimeouts, route.Consecutive5xx)
				route.CircuitState = "half_open"
			} else {
				return "cooldown"
			}
		} else {
			return "cooldown"
		}
	}
	if route.CircuitState == "half_open" {
		if g.halfOpenLocked(route.ID) {
			return "cooldown"
		}
		return "degraded"
	}
	if route.RouteState == "degraded" {
		return "degraded"
	}
	return "healthy"
}

func verificationStatus(route scheduler.Route, key *scheduler.ProviderKey, metrics scheduler.Metrics, eff string) string {
	return scheduler.VerificationStatus(route, key, metrics, eff)
}

type poolEval struct {
	PoolStatus      string
	Missing         []string
	Failed          []string
	Verified        []string
	Untested        []string
}

func (g *Gateway) evaluatePool(targets []string, routes []scheduler.Route, keys map[string]scheduler.ProviderKey) poolEval {
	ev := poolEval{Missing: []string{}, Failed: []string{}, Verified: []string{}, Untested: []string{}}
	for _, target := range targets {
		var matching []scheduler.Route
		for _, r := range routes {
			if r.Enabled && r.ModelID == target {
				matching = append(matching, r)
			}
		}
		if len(matching) == 0 {
			ev.Missing = append(ev.Missing, target)
			continue
		}
		statuses := []string{}
		for _, r := range matching {
			k, ok := keys[r.ProviderKeyID]
			var kp *scheduler.ProviderKey
			if ok {
				kp = &k
			}
			metrics, _ := g.Store.GetMetrics(r.ID)
			eff := g.effectiveStateWithHeal(r, kp)
			statuses = append(statuses, verificationStatus(r, kp, metrics, eff))
		}
		has := func(s string) bool {
			for _, x := range statuses {
				if x == s {
					return true
				}
			}
			return false
		}
		switch {
		case has("verified"):
			ev.Verified = append(ev.Verified, target)
		case has("untested"):
			ev.Untested = append(ev.Untested, target)
		case has("failed"):
			ev.Failed = append(ev.Failed, target)
		default:
			ev.Missing = append(ev.Missing, target)
		}
	}
	usable := len(ev.Verified) + len(ev.Untested)
	invalid := len(ev.Missing) + len(ev.Failed)
	switch {
	case len(targets) == 0 || usable == 0:
		ev.PoolStatus = "no_routes"
	case invalid > 0:
		ev.PoolStatus = "partial_invalid"
	case len(ev.Verified) > 0:
		ev.PoolStatus = "callable"
	default:
		ev.PoolStatus = "untested"
	}
	return ev
}

// ---- /v1/models ----

func (g *Gateway) HandleModels(w http.ResponseWriter, r *http.Request) {
	routes, keys, aliases, _, err := g.loadFleet()
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
		return
	}
	enabled := []scheduler.Route{}
	for _, rt := range routes {
		if rt.Enabled {
			enabled = append(enabled, rt)
		}
	}
	data := []interface{}{}
	for _, a := range aliases {
		ev := g.evaluatePool(a.TargetModels, enabled, keys)
		if ev.PoolStatus == "no_routes" {
			continue
		}
		data = append(data, map[string]interface{}{
			"id": a.Alias, "object": "model", "created": 1735689600, "owned_by": "portal-alias",
			"permission": []interface{}{}, "root": a.Alias, "parent": nil,
			"portal_type": "task_pool_alias", "upstream_model": strings.Join(a.TargetModels, " -> "),
			"verified": ev.PoolStatus == "callable",
			"capabilities": map[string]interface{}{"vision": true, "tools": true, "json_mode": true, "streaming": true, "max_context": 128000, "task_role": "auto", "description": a.Description, "target_routes": a.TargetModels},
			"health_status": ev.PoolStatus,
		})
	}
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
}

// ---- /v1/chat/completions ----

type chatRequest struct {
	Model    string                 `json:"model"`
	Messages []json.RawMessage      `json:"messages"`
	Tools    []json.RawMessage      `json:"tools"`
	Stream   bool                   `json:"stream"`
	MaxTokens *int                  `json:"max_tokens"`
	Raw      map[string]interface{} `json:"-"`
}

func (g *Gateway) HandleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		openAIError(w, 400, "failed to read body", "invalid_request_error", "bad_request")
		return
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		openAIError(w, 400, "invalid JSON body", "invalid_request_error", "bad_request")
		return
	}
	// Auth.
	bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	// TrimPrefix is case-sensitive; handle "bearer" too.
	if bearer == strings.TrimSpace(r.Header.Get("Authorization")) {
		// No Bearer prefix was present; bearer is the whole header, which
		// will not match a token, matching Node's behaviour (token lookup fails).
	}
	token, err := g.Store.GetTokenByValue(bearer)
	if err != nil || token == nil {
		openAIError(w, 401, "Invalid or missing Portal Gateway API token. Use a valid sk-portal-* token from the console.", "invalid_request_error", "invalid_api_key")
		return
	}
	if !token.Active {
		openAIError(w, 403, fmt.Sprintf("Token %q is currently disabled.", token.Name), "permission_error", "token_disabled")
		return
	}
	if token.QuotaLimit != nil && token.UsedTokens >= *token.QuotaLimit {
		openAIError(w, 429, fmt.Sprintf("Token %q has reached its token quota limit (%d / %d).", token.Name, token.UsedTokens, *token.QuotaLimit), "insufficient_quota", "quota_exceeded")
		return
	}
	routes, keys, aliases, cfg, err := g.loadFleet()
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
		return
	}
	if !g.checkRateLimit(token.ID, cfg.RateLimitRPM) {
		rpm := cfg.RateLimitRPM
		if rpm <= 0 {
			rpm = 120
		}
		openAIError(w, 429, fmt.Sprintf("Rate limit exceeded (%d RPM). Gateway circuit breaker paused request to protect upstream quota.", rpm), "rate_limit_error", "client_rate_limited")
		return
	}
	// Parse messages for classification (text parts only, image detection).
	msgs := parseMessages(raw["messages"])
	toolsLen := 0
	if t, ok := raw["tools"]; ok {
		if b, err := json.Marshal(t); err == nil {
			toolsLen = len(b)
		}
	}
	rfType := ""
	if rf, ok := raw["response_format"].(map[string]interface{}); ok {
		if t, ok := rf["type"].(string); ok {
			rfType = t
		}
	}
	cls := scheduler.Classify(scheduler.ClassifyInput{
		TaskHeader: r.Header.Get("X-Router-Task"), PrivateHeader: r.Header.Get("X-Router-Private"),
		Model: stringVal(raw["model"]), Messages: msgs, ToolsJSONLen: toolsLen,
		HasTools: toolsLen > 0, HasJSONMode: rfType == "json_object" || rfType == "json_schema",
		// Privacy mode is normalized to off/low/high here (and
		// defensively inside Classify): an empty or invalid stored
		// mode fails closed to off, per user order, not to high.
		PrivacyMode: scheduler.EffectivePrivacyMode(cfg.PrivacyMode), HeaderSession: r.Header.Get("X-Session-Id"),
		MetaSession: metaSession(raw), RequestID: newRequestID(),
	})
	// Model resolution.
	rawModel := strings.TrimSpace(stringVal(raw["model"]))
	requestedModel := rawModel
	isImplicit := rawModel == "" || rawModel == "default" || rawModel == "auto"
	if isImplicit {
		requestedModel = "coder"
	}
	explicitPin := strings.TrimSpace(r.Header.Get("X-Portal-Target-Model"))
	if explicitPin == "" && cfg.DispatchMode == "pinned" {
		explicitPin = cfg.PinnedModelID
	}
	var aliasTargets []string
	aliasByName := map[string]store.Alias{}
	for _, a := range aliases {
		aliasByName[a.Alias] = a
	}
	enabledRoutes := []scheduler.Route{}
	for _, rt := range routes {
		if rt.Enabled && rt.RouteState != "disabled" {
			enabledRoutes = append(enabledRoutes, rt)
		}
	}
	if explicitPin != "" {
		found := false
		for _, rt := range enabledRoutes {
			if rt.ModelID == explicitPin {
				found = true
			}
		}
		if !found {
			if a, ok := aliasByName[explicitPin]; ok && len(a.TargetModels) > 0 {
				aliasTargets = a.TargetModels
				explicitPin = ""
			}
		}
	}
	if explicitPin == "" && len(aliasTargets) == 0 && requestedModel != "" {
		if a, ok := aliasByName[requestedModel]; ok && len(a.TargetModels) > 0 {
			aliasTargets = a.TargetModels
		} else {
			for _, rt := range enabledRoutes {
				if rt.ModelID == requestedModel {
					aliasTargets = []string{requestedModel}
					break
				}
			}
		}
	}
	candidates := g.selectCandidates(cls, explicitPin, aliasTargets, enabledRoutes, keys, cfg)
	if len(candidates) == 0 && len(aliasTargets) > 0 && explicitPin == "" && isImplicit {
		candidates = g.selectCandidates(cls, "", nil, enabledRoutes, keys, cfg)
		if len(candidates) > 0 {
			w.Header().Set("x-gateway-alias-fallback", "auto")
		}
	}
	if len(candidates) == 0 {
		img := "否"
		if cls.HasImage {
			img = "是"
		}
		openAIError(w, 503, fmt.Sprintf("没有可用或健康的候选模型。当前任务需求: [%s], 私密需求: [%v], 需要图片支持: [%s]。请在控制台添加对应可用模型或检查 API Key 状态。", cls.TaskType, cls.IsPrivate, img), "no_healthy_route", "no_route_available")
		return
	}
	// Rotation: first pick only, failover keeps scored order.
	poolKey := requestedModel
	if explicitPin != "" {
		poolKey = explicitPin
	}
	ordered := candidates
	if explicitPin == "" && len(aliasTargets) > 0 {
		billedByKey := map[string]int{}
		for _, c := range candidates {
			q, _ := g.Store.GetQuota(c.Key.ID)
			billedByKey[c.Key.ID] = q.BilledTokensToday
		}
		kaggleID := ""
		for _, c := range candidates {
			if strings.Contains(c.Route.BaseURL, "mp-staging.kaggle.net") || c.Route.ModelID == "google/gemini-3-flash-preview" {
				// Kaggle main route identification: prefer the mp-staging
				// URL; the modelId fallback is documented in the contract
				// as the current production Kaggle alias target.
				kaggleID = c.Route.ModelID
				break
			}
		}
		ordered = g.Rotator.ReorderForRotation(candidates, poolKey, scheduler.SizeClassOf(cls.EstimatedTokens), kaggleID, billedByKey)
	}
	stream := false
	if v, ok := raw["stream"].(bool); ok {
		stream = v
	}
	// Try candidates in order, failover before first byte only.
	var chain []string
	var lastStatus int
	for _, cand := range ordered {
		// Half-open probe lock: only one trial at a time.
		if cand.Route.CircuitState == "half_open" {
			if !g.acquireHalfOpen(cand.Route.ID) {
				continue
			}
			defer g.releaseHalfOpen(cand.Route.ID)
		}
		tier := upstream.TimeoutTierFor(cand.Route.ID, cand.Route.Name, cand.Route.ModelID, cand.Route.UpstreamModel, cand.Route.BaseURL, cand.Route.TaskRole, stream)
		status, respBody, usage, upstreamErr := g.callUpstream(cand, bodyBytes, raw, stream, tier.TimeoutMs)
		latency := int(time.Since(start).Milliseconds())
		if upstreamErr != nil {
			chain = append(chain, fmt.Sprintf("%s (%d %s)", cand.Route.ModelID, status, upstreamErr.Category))
			g.applyFailure(cand, upstreamErr, status, latency)
			lastStatus = status
			continue
		}
		// Non-streaming repair, in Node adapters.ts order: tool-rescue
		// first, then tool-args, then the validator runs on the
		// repaired body. Streaming responses are passed through
		// untouched: rescue and args never touch a stream. Both
		// repairs fail open (original body unchanged on any doubt)
		// and never trigger failover by themselves; a validator
		// failure after repair still fails over, as in Node.
		if !stream {
			declared := []string{}
			toolDefs := []toolargs.ToolDef{}
			if t, ok := raw["tools"].([]interface{}); ok {
				for _, tool := range t {
					if tm, ok := tool.(map[string]interface{}); ok {
						if fn, ok := tm["function"].(map[string]interface{}); ok {
							if name, ok := fn["name"].(string); ok {
								declared = append(declared, name)
								var params map[string]interface{}
								if p, ok := fn["parameters"].(map[string]interface{}); ok {
									params = p
								}
								toolDefs = append(toolDefs, toolargs.ToolDef{Name: name, Parameters: params})
							}
						}
					}
				}
			}
			if repaired, _, _ := toolrescue.RescueResponse(respBody, declared); repaired != nil {
				respBody = repaired
			}
			if repaired, _, _ := toolargs.RepairResponse(respBody, toolDefs); repaired != nil {
				respBody = repaired
			}
			vr := validator.Validate(respBody, declared, rfType)
			if !vr.Valid {
				chain = append(chain, fmt.Sprintf("%s (%s)", cand.Route.ModelID, vr.Code))
				// Validation failure counts as an upstream failure for
				// failover, but does not penalise the route like a 5xx:
				// record the metric only (Node: validator failure -> failover).
				_ = g.Store.RecordFailure(cand.Route.ID, cand.Key.ID, vr.Code, vr.Reason, latency, false)
				lastStatus = 502
				continue
			}
		}
		// Success.
		billed := usage
		if billed <= 0 {
			billed = cls.EstimatedTokens
		}
		_ = g.Store.RecordSuccess(cand.Route.ID, cand.Key.ID, billed, 0, latency)
		_ = g.Store.ResetKey429(cand.Key.ID)
		if cls.IsExplicitSession {
			_ = g.Store.SetAffinity(cls.SessionKey, cand.Route.ID, cand.Key.ID, 1800)
		}
		_ = g.Store.IncrementTokenUsage(token.ID, billed)
		chain = append(chain, fmt.Sprintf("%s (200 校验通过)", cand.Route.ModelID))
		chainJSON, _ := json.Marshal(chain)
		_ = g.Store.AddAudit(store.AuditRow{
			RequestID: newRequestID(), Timestamp: time.Now().UTC().Format(time.RFC3339),
			SessionKey: cls.SessionKey, ClientTokenID: token.ID, ClientTokenName: token.Name,
			DetectedTaskType: cls.TaskType, IsPrivate: cls.IsPrivate,
			SelectedProviderKeyID: cand.Key.ID, SelectedRouteID: cand.Route.ID, SelectedModelID: cand.Route.ModelID,
			FailoverChainJSON: string(chainJSON), EstimatedTokens: cls.EstimatedTokens, BilledTokens: billed,
			UsageSource: "upstream", LatencyMs: latency, StatusCode: 200, ValidatorResult: "pass",
			PreviewPrompt: previewPrompt(msgs), RequestedModel: rawModel,
		})
		// Auto-enroll (Phase 5): a route that just succeeded is proven
		// working, so it appends at the last position of every alias
		// it is not already in (vision capability-filtered). This runs
		// only on confirmed success, never on failure or validator
		// failure, and a failed enrolment never breaks the response.
		_ = g.AutoEnrollRoute(cand.Route)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			_, _ = w.Write(respBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(respBody)
		return
	}
	_ = lastStatus
	openAIError(w, 502, "Upstream failed on all candidates: "+strings.Join(chain, "; "), "upstream_error", "all_candidates_failed")
}

func (g *Gateway) selectCandidates(cls scheduler.Classification, explicitPin string, aliasTargets []string, routes []scheduler.Route, keys map[string]scheduler.ProviderKey, cfg scheduler.SchedulerConfig) []scheduler.Candidate {
	if cls.RoutingMode == "auto" {
		aliasTargets = nil
	}
	wSticky, wRole, wRel, wCost, wLat := cfg.Weights()
	var out []scheduler.Candidate
	// Session affinity (explicit sessions only).
	var affRouteID string
	if cls.IsExplicitSession {
		if rid, _, ok, _ := g.Store.GetAffinity(cls.SessionKey); ok {
			affRouteID = rid
		}
	}
	for _, route := range routes {
		key, ok := keys[route.ProviderKeyID]
		if !ok {
			continue
		}
		eff := g.effectiveStateWithHeal(route, &key)
		switch eff {
		case "unconfigured", "disabled", "exhausted", "unavailable_for_key", "cooldown":
			continue
		}
		quota, _ := g.Store.GetQuota(key.ID)
		if !scheduler.HardFilter(cls, route, &quota) {
			continue
		}
		metrics, _ := g.Store.GetMetrics(route.ID)
		ver := verificationStatus(route, &key, metrics, eff)
		rRel := clamp01(float64(route.ReliabilityScore) / 100)
		lLat := clamp01(float64(metrics.LatencyP50) / 5000)
		cCost := clamp01(float64(route.CostWeight-1) / 9)
		if explicitPin != "" {
			if route.ModelID == explicitPin {
				score := 1000 + rRel*10 - lLat*5
				out = append(out, scheduler.Candidate{Route: route, Key: key, EffectiveState: eff, Score: score, TargetIdx: -1})
			}
			continue
		}
		if len(aliasTargets) > 0 {
			idx := -1
			for i, t := range aliasTargets {
				if t == route.ModelID {
					idx = i
					break
				}
			}
			if idx >= 0 {
				score, _ := scheduler.ComputeAliasScore(idx, &route.ReliabilityScore, metrics.LatencyP50, &quota, ver, route.MaxContext, route.BaseURL, cls.EstimatedTokens, cls.RequestLanguage, nil, true)
				out = append(out, scheduler.Candidate{Route: route, Key: key, EffectiveState: eff, Score: score, TargetIdx: idx})
			}
			continue
		}
		// Auto: sticky/role/reliability/cost/latency.
		isSticky := affRouteID != "" && affRouteID == route.ID
		sSticky := 0.0
		if isSticky {
			sSticky = 1
			if cls.TaskType == "reasoning" && route.TaskRole == "fast" {
				isSticky, sSticky = false, 0
			}
		}
		mRole := 0.35
		if route.TaskRole == cls.TaskType {
			mRole = 1
		} else if route.TaskRole == "auto" {
			mRole = 0.8
		}
		penalty := 0.0
		if eff == "degraded" {
			penalty = 0.3
		}
		score := wSticky*sSticky + wRole*mRole + wRel*rRel - wCost*cCost - wLat*lLat - penalty
		out = append(out, scheduler.Candidate{Route: route, Key: key, EffectiveState: eff, Score: score, IsSticky: isSticky, TargetIdx: -1})
	}
	// Sort descending by score, stable by original order (alias order
	// is preserved for equal scores because Go sort is stable here
	// only if we use a stable sort; see below).
	sortCandidates(out)
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (g *Gateway) applyFailure(cand scheduler.Candidate, uerr *upstream.Error, status int, latency int) {
	g.releaseHalfOpen(cand.Route.ID)
	// Metric only for non-penalising categories.
	switch uerr.Category {
	case upstream.CategoryUnsupportedParam, upstream.CategoryBadRequest, upstream.CategoryContextOverflow:
		_ = g.Store.RecordFailure(cand.Route.ID, cand.Key.ID, string(uerr.Category), uerr.Message, latency, false)
		return
	case upstream.CategoryRateLimited:
		if uerr.IsDailyQuota {
			tomorrow := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
			_ = g.Store.UpdateKeyState(cand.Key.ID, "exhausted", tomorrow.Format(time.RFC3339), false)
		} else {
			secs := 60
			if uerr.RetryAfter != nil && *uerr.RetryAfter > 0 {
				secs = *uerr.RetryAfter
			} else {
				n := cand.Key.Consecutive429 + 1
				secs = 60
				for i := 1; i < n; i++ {
					secs *= 2
					if secs >= 900 {
						secs = 900
						break
					}
				}
				if secs > 900 {
					secs = 900
				}
			}
			_ = g.Store.UpdateKeyState(cand.Key.ID, "cooldown", time.Now().Add(time.Duration(secs)*time.Second).Format(time.RFC3339), true)
		}
		_ = g.Store.RecordFailure(cand.Route.ID, cand.Key.ID, string(uerr.Category), uerr.Message, latency, false)
	case upstream.CategoryQuotaExhausted:
		_ = g.Store.UpdateKeyState(cand.Key.ID, "exhausted", "", false)
		_ = g.Store.RecordFailure(cand.Route.ID, cand.Key.ID, string(uerr.Category), uerr.Message, latency, false)
	case upstream.CategoryAuthInvalid:
		_ = g.Store.UpdateKeyState(cand.Key.ID, "disabled", "", false)
		_ = g.Store.RecordFailure(cand.Route.ID, cand.Key.ID, string(uerr.Category), uerr.Message, latency, false)
	case upstream.CategoryModelForbidden:
		_ = g.Store.UpdateRouteState(cand.Route.ID, "unavailable_for_key", time.Now().Add(12*time.Hour).Format(time.RFC3339))
		_ = g.Store.RecordFailure(cand.Route.ID, cand.Key.ID, string(uerr.Category), uerr.Message, latency, false)
	case upstream.CategoryTimeout, upstream.CategoryServerError:
		_ = g.Store.RecordFailure(cand.Route.ID, cand.Key.ID, string(uerr.Category), uerr.Message, latency, true)
	default:
		_ = g.Store.RecordFailure(cand.Route.ID, cand.Key.ID, string(uerr.Category), uerr.Message, latency, false)
	}
}

// callUpstream forwards to the route's base URL + /chat/completions
// with the route's upstream model substituted. It returns
// (status, body, billedTokens, upstreamError). Streaming bodies are
// returned whole (the caller re-streams them); chunk-level watchdog
// semantics are a documented Phase 3 gap for SSE (see contract).
func (g *Gateway) callUpstream(cand scheduler.Candidate, origBody []byte, raw map[string]interface{}, stream bool, timeoutMs int) (int, []byte, int, *upstream.Error) {
	key, err := g.decrypt(cand.Key.EncryptedKey)
	if err != nil {
		return 0, nil, 0, &upstream.Error{Category: upstream.CategoryAuthInvalid, Message: "stored key could not be decrypted"}
	}
	reqBody := map[string]interface{}{}
	_ = json.Unmarshal(origBody, &reqBody)
	reqBody["model"] = cand.Route.UpstreamModel
	// Remove gateway-only fields if present.
	delete(reqBody, "metadata")
	fwd, _ := json.Marshal(reqBody)
	base := strings.TrimRight(cand.Route.BaseURL, "/")
	url := base + "/chat/completions"
	// Base URLs in Node already point at /v1; a base ending in /v1
	// yields /v1/chat/completions, which is correct. A base that
	// already ends in /chat/completions is left alone.
	if strings.HasSuffix(base, "/chat/completions") {
		url = base
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(fwd))
	if err != nil {
		return 0, nil, 0, &upstream.Error{Category: upstream.CategoryServerError, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		// Timeout detection: net.Error with Timeout() true.
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return 0, nil, 0, &upstream.Error{Category: upstream.CategoryTimeout, Message: err.Error()}
		}
		return 0, nil, 0, &upstream.Error{Category: upstream.CategoryServerError, Message: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Parse error param/code for normalisation.
		var parsed struct {
			Error struct {
				Param string `json:"param"`
				Code  string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &parsed)
		uerr := upstream.Normalize(resp.StatusCode, string(body), resp.Header, strings.ToLower(parsed.Error.Param), strings.ToLower(parsed.Error.Code))
		return resp.StatusCode, nil, 0, uerr
	}
	billed := 0
	if !stream {
		var parsed struct {
			Usage *struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(body, &parsed) == nil && parsed.Usage != nil {
			billed = parsed.Usage.TotalTokens
		}
	}
	return resp.StatusCode, body, billed, nil
}

// ---- parsing helpers ----

func stringVal(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func metaSession(raw map[string]interface{}) string {
	if md, ok := raw["metadata"].(map[string]interface{}); ok {
		for _, k := range []string{"conversation_id", "thread_id", "project_id"} {
			if s := stringVal(md[k]); s != "" {
				return s
			}
		}
	}
	for _, k := range []string{"conversation_id", "thread_id", "user"} {
		if s := stringVal(raw[k]); s != "" {
			return s
		}
	}
	return ""
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("req_%x", b)
}

func parseMessages(v interface{}) []scheduler.Message {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := []scheduler.Message{}
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		msg := scheduler.Message{Role: stringVal(m["role"])}
		switch c := m["content"].(type) {
		case string:
			msg.Content = c
		case []interface{}:
			parts := []scheduler.ContentPart{}
			for _, p := range c {
				pm, ok := p.(map[string]interface{})
				if !ok {
					continue
				}
				part := scheduler.ContentPart{Type: stringVal(pm["type"]), Text: stringVal(pm["text"])}
				if iu, ok := pm["image_url"].(map[string]interface{}); ok {
					part.ImageURL = &struct {
						URL string `json:"url"`
					}{URL: stringVal(iu["url"])}
				}
				parts = append(parts, part)
			}
			msg.Content = parts
		}
		out = append(out, msg)
	}
	return out
}

func previewPrompt(msgs []scheduler.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			t := msgs[i].Text()
			if len(t) > 100 {
				t = t[:100]
			}
			if msgs[i].HasImage() {
				return "[Image Redacted] " + t
			}
			return t
		}
	}
	if len(msgs) > 0 {
		t := msgs[len(msgs)-1].Text()
		if len(t) > 100 {
			t = t[:100]
		}
		return t
	}
	return ""
}

func sortCandidates(c []scheduler.Candidate) {
	// Stable insertion sort (candidate lists are small).
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].Score > c[j-1].Score; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}

func intQuery(r *http.Request, name string, def, min, max int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
