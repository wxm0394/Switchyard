package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wxm0394/Switchyard/internal/scheduler"
)

const routeCols = `id, name, provider_key_id, model_id, display_name, base_url, provider_type, upstream_model, task_role, route_state, circuit_state, circuit_opens_until, consecutive_timeouts, consecutive_5xx, consecutive_failures, circuit_open_count, stable_success_count, circuit_closed_at, unavailable_until, supports_vision, supports_tools, supports_json_mode, supports_streaming, max_context, allow_sensitive, private_only, privacy_tier, cost_weight, reliability_score, enabled, created_at`

func scanRouteRow(row interface{ Scan(...interface{}) error }) (scheduler.Route, error) {
	var r scheduler.Route
	var co, cca, uu sql.NullString
	var ssc int
	var created string
	var sv, st, jm, ss, as, po, en int
	err := row.Scan(&r.ID, &r.Name, &r.ProviderKeyID, &r.ModelID, &r.DisplayName, &r.BaseURL, &r.ProviderType, &r.UpstreamModel, &r.TaskRole, &r.RouteState, &r.CircuitState, &co, &r.ConsecutiveTimeouts, &r.Consecutive5xx, &r.ConsecutiveFailures, &r.CircuitOpenCount, &ssc, &cca, &uu, &sv, &st, &jm, &ss, &r.MaxContext, &as, &po, &r.PrivacyTier, &r.CostWeight, &r.ReliabilityScore, &en, &created)
	if err != nil {
		return r, err
	}
	r.CircuitOpensUntil = co.String
	_ = cca
	r.UnavailableUntil = uu.String
	r.SupportsVision, r.SupportsTools, r.SupportsJSONMode, r.SupportsStreaming = intToBool(sv), intToBool(st), intToBool(jm), intToBool(ss)
	r.AllowSensitive, r.PrivateOnly, r.Enabled = intToBool(as), intToBool(po), intToBool(en)
	return r, nil
}

func (s *SQLiteStore) ListRoutes() ([]scheduler.Route, error) {
	rows, err := s.db.Query("SELECT " + routeCols + " FROM model_routes ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduler.Route{}
	for rows.Next() {
		r, err := scanRouteRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListKeys() ([]scheduler.ProviderKey, error) {
	rows, err := s.db.Query(`SELECT id, name, provider_type, encrypted_key, key_mask, key_state, cooldown_until, consecutive_429, base_url FROM provider_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduler.ProviderKey{}
	for rows.Next() {
		var k scheduler.ProviderKey
		var co sql.NullString
		if err := rows.Scan(&k.ID, &k.Name, &k.ProviderType, &k.EncryptedKey, &k.KeyMask, &k.KeyState, &co, &k.Consecutive429, &k.BaseURL); err != nil {
			return nil, err
		}
		k.CooldownUntil = co.String
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListAliases() ([]Alias, error) {
	rows, err := s.db.Query(`SELECT id, alias, target_models_json, description, created_at FROM model_aliases ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alias{}
	for rows.Next() {
		var a Alias
		var tj, desc sql.NullString
		if err := rows.Scan(&a.ID, &a.Alias, &tj, &desc, &a.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tj.String), &a.TargetModels)
		a.Description = desc.String
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetAlias(name string) (*Alias, error) {
	var a Alias
	var tj, desc sql.NullString
	err := s.db.QueryRow(`SELECT id, alias, target_models_json, description, created_at FROM model_aliases WHERE alias=?`, name).Scan(&a.ID, &a.Alias, &tj, &desc, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tj.String), &a.TargetModels)
	a.Description = desc.String
	return &a, nil
}

func (s *SQLiteStore) ListTokens() ([]ClientToken, error) {
	rows, err := s.db.Query(`SELECT id, name, token, created_at, last_used_at, active, quota_limit, used_tokens, request_count, note FROM client_tokens ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientToken{}
	for rows.Next() {
		var t ClientToken
		var lu sql.NullString
		var ql sql.NullInt64
		var act int
		if err := rows.Scan(&t.ID, &t.Name, &t.Token, &t.CreatedAt, &lu, &act, &ql, &t.UsedTokens, &t.RequestCount, &t.Note); err != nil {
			return nil, err
		}
		t.LastUsedAt = lu.String
		t.Active = intToBool(act)
		if ql.Valid {
			v := int(ql.Int64)
			t.QuotaLimit = &v
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetTokenByValue(v string) (*ClientToken, error) {
	var t ClientToken
	var lu sql.NullString
	var ql sql.NullInt64
	var act int
	err := s.db.QueryRow(`SELECT id, name, token, created_at, last_used_at, active, quota_limit, used_tokens, request_count, note FROM client_tokens WHERE token=?`, v).Scan(&t.ID, &t.Name, &t.Token, &t.CreatedAt, &lu, &act, &ql, &t.UsedTokens, &t.RequestCount, &t.Note)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.LastUsedAt = lu.String
	t.Active = intToBool(act)
	if ql.Valid {
		x := int(ql.Int64)
		t.QuotaLimit = &x
	}
	return &t, nil
}

func (s *SQLiteStore) GetConfig() (scheduler.SchedulerConfig, error) {
	var c scheduler.SchedulerConfig
	var pinned sql.NullString
	err := s.db.QueryRow(`SELECT dispatch_mode, pinned_model_id, weights_json, rate_limit_rpm, privacy_mode FROM scheduler_config WHERE id='default'`).Scan(&c.DispatchMode, &pinned, &c.WeightsJSON, &c.RateLimitRPM, &c.PrivacyMode)
	if err != nil {
		return c, err
	}
	c.PinnedModelID = pinned.String
	return c, nil
}

func (s *SQLiteStore) UpdateConfig(patch map[string]interface{}) (scheduler.SchedulerConfig, error) {
	cfg, err := s.GetConfig()
	if err != nil {
		return cfg, err
	}
	if v, ok := patch["privacyMode"].(string); ok && (v == "off" || v == "low" || v == "high") {
		cfg.PrivacyMode = v
	}
	if v, ok := patch["dispatchMode"].(string); ok && (v == "auto" || v == "pinned") {
		cfg.DispatchMode = v
	}
	if v, ok := patch["pinnedModelId"].(string); ok {
		cfg.PinnedModelID = v
	}
	if v, ok := patch["weightsJson"].(string); ok {
		cfg.WeightsJSON = v
	}
	if v, ok := patch["rateLimitRpm"].(float64); ok {
		cfg.RateLimitRPM = int(v)
	}
	_, err = s.db.Exec(`UPDATE scheduler_config SET dispatch_mode=?, pinned_model_id=?, weights_json=?, rate_limit_rpm=?, privacy_mode=?, updated_at=? WHERE id='default'`, cfg.DispatchMode, nullString(cfg.PinnedModelID), cfg.WeightsJSON, cfg.RateLimitRPM, cfg.PrivacyMode, time.Now().Format(time.RFC3339))
	return cfg, err
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func (s *SQLiteStore) GetMetrics(routeID string) (scheduler.Metrics, error) {
	var m scheduler.Metrics
	var lec sql.NullString
	err := s.db.QueryRow(`SELECT latency_p50, latency_p95, success_count, failure_count, last_error_code FROM route_metrics_snapshots WHERE route_id=?`, routeID).Scan(&m.LatencyP50, &m.LatencyP95, &m.SuccessCount, &m.FailureCount, &lec)
	if err == sql.ErrNoRows {
		return scheduler.Metrics{LatencyP50: 450, LatencyP95: 1200}, nil
	}
	if err != nil {
		return m, err
	}
	m.LastErrorCode = lec.String
	return m, nil
}

func (s *SQLiteStore) GetQuota(keyID string) (scheduler.Quota, error) {
	var q scheduler.Quota
	var lim sql.NullInt64
	date := time.Now().UTC().Format("2006-01-02")
	err := s.db.QueryRow(`SELECT daily_token_limit, billed_tokens_today FROM quota_counters WHERE key_id=? AND window_date=?`, keyID, date).Scan(&lim, &q.BilledTokensToday)
	if err == sql.ErrNoRows {
		return scheduler.Quota{}, nil
	}
	if err != nil {
		return q, err
	}
	if lim.Valid {
		v := int(lim.Int64)
		q.DailyTokenLimit = &v
	}
	return q, nil
}

func (s *SQLiteStore) GetAffinity(k string) (string, string, bool, error) {
	var routeID, keyID, exp string
	err := s.db.QueryRow(`SELECT route_id, provider_key_id, expires_at FROM session_affinity WHERE session_key=?`, k).Scan(&routeID, &keyID, &exp)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if t, err := time.Parse(time.RFC3339, exp); err == nil && time.Now().After(t) {
		return "", "", false, nil
	}
	return routeID, keyID, true, nil
}

func (s *SQLiteStore) SetAffinity(k, routeID, keyID string, ttl int) error {
	exp := time.Now().Add(time.Duration(ttl) * time.Second).Format(time.RFC3339)
	_, err := s.db.Exec(`INSERT INTO session_affinity (session_key, route_id, provider_key_id, hit_count, expires_at) VALUES (?,?,?,?,?) ON CONFLICT(session_key) DO UPDATE SET route_id=excluded.route_id, provider_key_id=excluded.provider_key_id, hit_count=session_affinity.hit_count+1, expires_at=excluded.expires_at`, k, routeID, keyID, exp)
	return err
}

func (s *SQLiteStore) RecordSuccess(routeID, keyID string, billed, fallback, latency int) error {
	now := time.Now().Format(time.RFC3339)
	date := time.Now().UTC().Format("2006-01-02")
	_, err := s.db.Exec(`UPDATE model_routes SET consecutive_failures=0, circuit_state='closed', stable_success_count=stable_success_count+1, circuit_closed_at=? WHERE id=?`, now, routeID)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`INSERT INTO route_metrics_snapshots (route_id, latency_p50, latency_p95, recent_latencies_json, success_count, failure_count, updated_at) VALUES (?,?,1200,'[]',1,0,?) ON CONFLICT(route_id) DO UPDATE SET success_count=success_count+1, latency_p50=excluded.latency_p50, last_error_code=NULL, last_error_message=NULL, updated_at=excluded.updated_at`, routeID, latency, now)
	_, _ = s.db.Exec(`INSERT INTO quota_counters (key_id, window_date, billed_tokens_today, fallback_tokens_today) VALUES (?,?,?,?) ON CONFLICT(key_id, window_date) DO UPDATE SET billed_tokens_today=billed_tokens_today+excluded.billed_tokens_today, fallback_tokens_today=fallback_tokens_today+excluded.fallback_tokens_today`, keyID, date, billed, fallback)
	_, _ = s.db.Exec(`UPDATE provider_keys SET consecutive_429=0 WHERE id=?`, keyID)
	return nil
}

func (s *SQLiteStore) RecordFailure(routeID, keyID, code, msg string, latency int, infra bool) error {
	now := time.Now().Format(time.RFC3339)
	_, _ = s.db.Exec(`INSERT INTO route_metrics_snapshots (route_id, latency_p50, latency_p95, recent_latencies_json, success_count, failure_count, last_error_code, last_error_message, updated_at) VALUES (?,450,1200,'[]',0,1,?,?,?) ON CONFLICT(route_id) DO UPDATE SET failure_count=failure_count+1, last_error_code=excluded.last_error_code, last_error_message=excluded.last_error_message, updated_at=excluded.updated_at`, routeID, code, msg, now)
	if !infra {
		return nil
	}
	var failures, openCount int
	_ = s.db.QueryRow(`SELECT consecutive_failures, circuit_open_count FROM model_routes WHERE id=?`, routeID).Scan(&failures, &openCount)
	failures++
	if failures >= 3 {
		secs := 60
		for i := 1; i <= openCount; i++ {
			secs *= 2
			if secs >= 900 {
				secs = 900
				break
			}
		}
		if secs > 900 {
			secs = 900
		}
		opens := time.Now().Add(time.Duration(secs) * time.Second).Format(time.RFC3339)
		_, err := s.db.Exec(`UPDATE model_routes SET consecutive_failures=?, circuit_state='open', circuit_opens_until=?, circuit_open_count=circuit_open_count+1 WHERE id=?`, failures, opens, routeID)
		return err
	}
	_, err := s.db.Exec(`UPDATE model_routes SET consecutive_failures=? WHERE id=?`, failures, routeID)
	return err
}

func (s *SQLiteStore) UpdateKeyState(keyID, state, cooldown string, inc429 bool) error {
	if inc429 {
		_, err := s.db.Exec(`UPDATE provider_keys SET key_state=?, cooldown_until=?, consecutive_429=consecutive_429+1 WHERE id=?`, state, nullString(cooldown), keyID)
		return err
	}
	_, err := s.db.Exec(`UPDATE provider_keys SET key_state=?, cooldown_until=? WHERE id=?`, state, nullString(cooldown), keyID)
	return err
}

func (s *SQLiteStore) UpdateRouteState(routeID, state, until string) error {
	_, err := s.db.Exec(`UPDATE model_routes SET route_state=?, unavailable_until=? WHERE id=?`, state, nullString(until), routeID)
	return err
}

// DisableRoute permanently takes a route out of the candidate pool by
// clearing its enabled flag. Used for models that are gone for good
// (HTTP 410). An admin can re-enable the route from the panel if the
// model ever comes back.
func (s *SQLiteStore) DisableRoute(routeID string) error {
	_, err := s.db.Exec(`UPDATE model_routes SET enabled=0 WHERE id=?`, routeID)
	return err
}

func (s *SQLiteStore) UpdateCircuit(routeID, state, opens string, timeouts, fivexx int) error {
	if state == "closed" {
		_, err := s.db.Exec(`UPDATE model_routes SET circuit_state=?, circuit_opens_until=?, consecutive_timeouts=?, consecutive_5xx=?, consecutive_failures=0 WHERE id=?`, state, nullString(opens), timeouts, fivexx, routeID)
		return err
	}
	_, err := s.db.Exec(`UPDATE model_routes SET circuit_state=?, circuit_opens_until=?, consecutive_timeouts=?, consecutive_5xx=? WHERE id=?`, state, nullString(opens), timeouts, fivexx, routeID)
	return err
}

func (s *SQLiteStore) AddAudit(row AuditRow) error {
	_, err := s.db.Exec(`INSERT INTO audit_logs (request_id, timestamp, session_key, client_token_id, client_token_name, detected_task_type, is_private, selected_provider_key_id, selected_route_id, selected_model_id, failover_chain_json, estimated_tokens, billed_tokens, usage_source, latency_ms, status_code, validator_result, preview_prompt, requested_model) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, row.RequestID, row.Timestamp, row.SessionKey, row.ClientTokenID, row.ClientTokenName, row.DetectedTaskType, boolToInt(row.IsPrivate), row.SelectedProviderKeyID, row.SelectedRouteID, row.SelectedModelID, row.FailoverChainJSON, row.EstimatedTokens, row.BilledTokens, row.UsageSource, row.LatencyMs, row.StatusCode, row.ValidatorResult, row.PreviewPrompt, row.RequestedModel)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM audit_logs WHERE request_id NOT IN (SELECT request_id FROM audit_logs ORDER BY timestamp DESC, rowid DESC LIMIT 200)`)
	return nil
}

func (s *SQLiteStore) ListAudit(limit, offset int) ([]AuditRow, error) {
	rows, err := s.db.Query(`SELECT request_id, timestamp, session_key, client_token_id, client_token_name, detected_task_type, is_private, selected_provider_key_id, selected_route_id, selected_model_id, failover_chain_json, estimated_tokens, billed_tokens, usage_source, latency_ms, status_code, validator_result, preview_prompt, requested_model FROM audit_logs ORDER BY timestamp DESC, rowid DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var r AuditRow
		var priv int
		var req sql.NullString
		if err := rows.Scan(&r.RequestID, &r.Timestamp, &r.SessionKey, &r.ClientTokenID, &r.ClientTokenName, &r.DetectedTaskType, &priv, &r.SelectedProviderKeyID, &r.SelectedRouteID, &r.SelectedModelID, &r.FailoverChainJSON, &r.EstimatedTokens, &r.BilledTokens, &r.UsageSource, &r.LatencyMs, &r.StatusCode, &r.ValidatorResult, &r.PreviewPrompt, &req); err != nil {
			return nil, err
		}
		r.IsPrivate = intToBool(priv)
		r.RequestedModel = req.String
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpsertKey(k scheduler.ProviderKey) error {
	_, err := s.db.Exec(`INSERT INTO provider_keys (id, name, provider_type, encrypted_key, key_mask, key_state, cooldown_until, consecutive_429, base_url, created_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, provider_type=excluded.provider_type, encrypted_key=excluded.encrypted_key, key_mask=excluded.key_mask, key_state=excluded.key_state, base_url=excluded.base_url`, k.ID, k.Name, k.ProviderType, k.EncryptedKey, k.KeyMask, k.KeyState, nullString(k.CooldownUntil), k.Consecutive429, k.BaseURL, time.Now().Format(time.RFC3339))
	return err
}
func (s *SQLiteStore) DeleteKey(id string) error {
	_, err := s.db.Exec(`DELETE FROM provider_keys WHERE id=?`, id)
	return err
}
func (s *SQLiteStore) UpsertRoute(r scheduler.Route) error {
	_, err := s.db.Exec(`INSERT INTO model_routes (id, name, provider_key_id, model_id, display_name, base_url, provider_type, upstream_model, task_role, route_state, circuit_state, circuit_opens_until, consecutive_timeouts, consecutive_5xx, consecutive_failures, circuit_open_count, supports_vision, supports_tools, supports_json_mode, supports_streaming, max_context, allow_sensitive, private_only, privacy_tier, cost_weight, reliability_score, enabled, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, provider_key_id=excluded.provider_key_id, model_id=excluded.model_id, display_name=excluded.display_name, base_url=excluded.base_url, provider_type=excluded.provider_type, upstream_model=excluded.upstream_model, task_role=excluded.task_role, max_context=excluded.max_context, allow_sensitive=excluded.allow_sensitive, private_only=excluded.private_only, enabled=excluded.enabled`,
		r.ID, r.Name, r.ProviderKeyID, r.ModelID, r.DisplayName, r.BaseURL, r.ProviderType, r.UpstreamModel, r.TaskRole, r.RouteState, r.CircuitState, nullString(r.CircuitOpensUntil), r.ConsecutiveTimeouts, r.Consecutive5xx, r.ConsecutiveFailures, r.CircuitOpenCount, boolToInt(r.SupportsVision), boolToInt(r.SupportsTools), boolToInt(r.SupportsJSONMode), boolToInt(r.SupportsStreaming), r.MaxContext, boolToInt(r.AllowSensitive), boolToInt(r.PrivateOnly), r.PrivacyTier, r.CostWeight, r.ReliabilityScore, boolToInt(r.Enabled), time.Now().Format(time.RFC3339))
	return err
}
func (s *SQLiteStore) DeleteRoute(id string) error {
	_, err := s.db.Exec(`DELETE FROM model_routes WHERE id=?`, id)
	return err
}
func (s *SQLiteStore) UpsertToken(t ClientToken) error {
	var ql interface{}
	if t.QuotaLimit != nil {
		ql = *t.QuotaLimit
	}
	_, err := s.db.Exec(`INSERT INTO client_tokens (id, name, token, created_at, last_used_at, active, quota_limit, used_tokens, request_count, note) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, active=excluded.active, quota_limit=excluded.quota_limit, note=excluded.note`, t.ID, t.Name, t.Token, t.CreatedAt, nullString(t.LastUsedAt), boolToInt(t.Active), ql, t.UsedTokens, t.RequestCount, t.Note)
	return err
}
func (s *SQLiteStore) DeleteToken(id string) error {
	_, err := s.db.Exec(`DELETE FROM client_tokens WHERE id=?`, id)
	return err
}
func (s *SQLiteStore) UpsertAlias(a Alias) error {
	tj, _ := json.Marshal(a.TargetModels)
	_, err := s.db.Exec(`INSERT INTO model_aliases (id, alias, target_models_json, description, created_at) VALUES (?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET alias=excluded.alias, target_models_json=excluded.target_models_json, description=excluded.description`, a.ID, a.Alias, string(tj), a.Description, time.Now().Format(time.RFC3339))
	if err != nil {
		// Alias unique constraint on alias name: fall back to update by alias.
		_, err = s.db.Exec(`UPDATE model_aliases SET target_models_json=?, description=? WHERE alias=?`, string(tj), a.Description, a.Alias)
	}
	return err
}
func (s *SQLiteStore) DeleteAlias(id string) error {
	_, err := s.db.Exec(`DELETE FROM model_aliases WHERE id=?`, id)
	return err
}
func (s *SQLiteStore) GetKeyByName(name string) (scheduler.ProviderKey, bool, error) {
	var k scheduler.ProviderKey
	var co sql.NullString
	err := s.db.QueryRow(`SELECT id, name, provider_type, encrypted_key, key_mask, key_state, cooldown_until, consecutive_429, base_url FROM provider_keys WHERE name=? LIMIT 1`, name).Scan(&k.ID, &k.Name, &k.ProviderType, &k.EncryptedKey, &k.KeyMask, &k.KeyState, &co, &k.Consecutive429, &k.BaseURL)
	if err == sql.ErrNoRows {
		return scheduler.ProviderKey{}, false, nil
	}
	if err != nil {
		return scheduler.ProviderKey{}, false, err
	}
	k.CooldownUntil = co.String
	return k, true, nil
}
func (s *SQLiteStore) GetKaggleState(providerKeyID string) (KaggleState, bool, error) {
	var st KaggleState
	var expires, lastRefresh, lastErr sql.NullString
	err := s.db.QueryRow(`SELECT provider_key_id, expires_at, last_refresh_at, last_error, consecutive_failures FROM kaggle_refresh_state WHERE provider_key_id=?`, providerKeyID).Scan(&st.ProviderKeyID, &expires, &lastRefresh, &lastErr, &st.ConsecutiveFailures)
	if err == sql.ErrNoRows {
		return KaggleState{}, false, nil
	}
	if err != nil {
		return KaggleState{}, false, err
	}
	st.ExpiresAt = expires.String
	st.LastRefreshAt = lastRefresh.String
	st.LastError = lastErr.String
	return st, true, nil
}
func (s *SQLiteStore) UpsertKaggleState(state KaggleState) error {
	_, err := s.db.Exec(`INSERT INTO kaggle_refresh_state (provider_key_id, expires_at, last_refresh_at, last_error, consecutive_failures) VALUES (?,?,?,?,?) ON CONFLICT(provider_key_id) DO UPDATE SET expires_at=excluded.expires_at, last_refresh_at=excluded.last_refresh_at, last_error=excluded.last_error, consecutive_failures=excluded.consecutive_failures`,
		state.ProviderKeyID, nullString(state.ExpiresAt), nullString(state.LastRefreshAt), nullString(state.LastError), state.ConsecutiveFailures)
	return err
}
func (s *SQLiteStore) UpdateKaggleKey(providerKeyID, encryptedKey, keyMask, expiresAt, lastRefreshAt string) error {
	if _, err := s.db.Exec(`UPDATE provider_keys SET encrypted_key=?, key_mask=?, key_state='healthy', cooldown_until=NULL WHERE id=?`, encryptedKey, keyMask, providerKeyID); err != nil {
		return err
	}
	return s.UpsertKaggleState(KaggleState{
		ProviderKeyID: providerKeyID, ExpiresAt: expiresAt,
		LastRefreshAt: lastRefreshAt, LastError: "", ConsecutiveFailures: 0,
	})
}
func (s *SQLiteStore) ResetKey429(keyID string) error {
	_, err := s.db.Exec(`UPDATE provider_keys SET consecutive_429=0 WHERE id=?`, keyID)
	return err
}
func (s *SQLiteStore) IncrementTokenUsage(tokenID string, billed int) error {
	_, err := s.db.Exec(`UPDATE client_tokens SET used_tokens=used_tokens+?, request_count=request_count+1, last_used_at=? WHERE id=?`, billed, time.Now().Format(time.RFC3339), tokenID)
	return err
}

var _ = fmt.Sprintf
var _ = strings.TrimSpace
