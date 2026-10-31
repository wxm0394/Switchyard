// Package store implements the Node Railways SQLite schema
// (server/db.ts) and a Store interface used by the gateway.
// Pragmas match Node: WAL, synchronous=NORMAL, foreign_keys=ON,
// cache_size=-4000 (4 MB), temp_store=MEMORY, mmap_size=0.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wxm0394/Switchyard/internal/scheduler"
	_ "modernc.org/sqlite"
)

const Schema = `
CREATE TABLE IF NOT EXISTS provider_keys (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, provider_type TEXT NOT NULL,
  encrypted_key TEXT NOT NULL, key_mask TEXT NOT NULL,
  key_state TEXT NOT NULL DEFAULT 'healthy', cooldown_until TEXT,
  consecutive_429 INTEGER NOT NULL DEFAULT 0,
  account_notes TEXT NOT NULL DEFAULT '', appeal_url TEXT NOT NULL DEFAULT '',
  base_url TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS kaggle_refresh_state (
  provider_key_id TEXT PRIMARY KEY, expires_at TEXT, last_refresh_at TEXT,
  last_error TEXT, consecutive_failures INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS model_routes (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, provider_key_id TEXT NOT NULL,
  model_id TEXT NOT NULL, display_name TEXT NOT NULL, base_url TEXT NOT NULL,
  provider_type TEXT NOT NULL, upstream_model TEXT NOT NULL,
  task_role TEXT NOT NULL DEFAULT 'auto', route_state TEXT NOT NULL DEFAULT 'healthy',
  circuit_state TEXT NOT NULL DEFAULT 'closed', circuit_opens_until TEXT,
  consecutive_timeouts INTEGER NOT NULL DEFAULT 0, consecutive_5xx INTEGER NOT NULL DEFAULT 0,
  consecutive_failures INTEGER NOT NULL DEFAULT 0, circuit_open_count INTEGER NOT NULL DEFAULT 0,
  stable_success_count INTEGER NOT NULL DEFAULT 0, circuit_closed_at TEXT, unavailable_until TEXT,
  supports_vision INTEGER NOT NULL DEFAULT 0, supports_tools INTEGER NOT NULL DEFAULT 1,
  supports_json_mode INTEGER NOT NULL DEFAULT 1, supports_streaming INTEGER NOT NULL DEFAULT 1,
  max_context INTEGER NOT NULL DEFAULT 128000, allow_sensitive INTEGER NOT NULL DEFAULT 0,
  private_only INTEGER NOT NULL DEFAULT 0, privacy_tier TEXT NOT NULL DEFAULT 'cloud_zero_retention',
  cost_weight INTEGER NOT NULL DEFAULT 1, reliability_score INTEGER NOT NULL DEFAULT 100,
  enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS quota_counters (
  key_id TEXT NOT NULL, window_date TEXT NOT NULL,
  rpm_count INTEGER NOT NULL DEFAULT 0, rpm_window_minute INTEGER NOT NULL DEFAULT 0,
  tpm_count INTEGER NOT NULL DEFAULT 0, rpd_count INTEGER NOT NULL DEFAULT 0,
  billed_tokens_today INTEGER NOT NULL DEFAULT 0, fallback_tokens_today INTEGER NOT NULL DEFAULT 0,
  daily_token_limit INTEGER, rpm_limit INTEGER, rpd_limit INTEGER,
  PRIMARY KEY (key_id, window_date));
CREATE TABLE IF NOT EXISTS route_metrics_snapshots (
  route_id TEXT PRIMARY KEY, latency_p50 INTEGER NOT NULL DEFAULT 450,
  latency_p95 INTEGER NOT NULL DEFAULT 1200, recent_latencies_json TEXT NOT NULL DEFAULT '[]',
  success_count INTEGER NOT NULL DEFAULT 0, failure_count INTEGER NOT NULL DEFAULT 0,
  last_error_code TEXT, last_error_message TEXT, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS session_affinity (
  session_key TEXT PRIMARY KEY, route_id TEXT NOT NULL, provider_key_id TEXT NOT NULL,
  hit_count INTEGER NOT NULL DEFAULT 1, expires_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scheduler_config (
  id TEXT PRIMARY KEY, dispatch_mode TEXT NOT NULL DEFAULT 'auto', pinned_model_id TEXT,
  weights_json TEXT NOT NULL, rate_limit_rpm INTEGER NOT NULL DEFAULT 120,
  ip_allowlist_json TEXT NOT NULL DEFAULT '[]', loopback_only INTEGER NOT NULL DEFAULT 0,
  privacy_mode TEXT NOT NULL DEFAULT 'high', updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS client_tokens (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, token TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL, last_used_at TEXT, active INTEGER NOT NULL DEFAULT 1,
  quota_limit INTEGER, used_tokens INTEGER NOT NULL DEFAULT 0,
  request_count INTEGER NOT NULL DEFAULT 0, note TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS audit_logs (
  request_id TEXT PRIMARY KEY, timestamp TEXT NOT NULL, session_key TEXT NOT NULL,
  client_token_id TEXT NOT NULL, client_token_name TEXT NOT NULL,
  detected_task_type TEXT NOT NULL, is_private INTEGER NOT NULL DEFAULT 0,
  selected_provider_key_id TEXT NOT NULL, selected_route_id TEXT NOT NULL,
  selected_model_id TEXT NOT NULL, failover_chain_json TEXT NOT NULL DEFAULT '[]',
  estimated_tokens INTEGER NOT NULL DEFAULT 0, billed_tokens INTEGER NOT NULL DEFAULT 0,
  usage_source TEXT NOT NULL DEFAULT 'upstream', latency_ms INTEGER NOT NULL DEFAULT 0,
  ttft_ms INTEGER, status_code INTEGER NOT NULL, validator_result TEXT NOT NULL DEFAULT 'pass',
  preview_prompt TEXT NOT NULL DEFAULT '', requested_model TEXT);
CREATE TABLE IF NOT EXISTS model_aliases (
  id TEXT PRIMARY KEY, alias TEXT NOT NULL UNIQUE, target_models_json TEXT NOT NULL,
  description TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')));
`

const migrations = `
ALTER TABLE provider_keys ADD COLUMN base_url TEXT NOT NULL DEFAULT '';
ALTER TABLE model_routes ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_routes ADD COLUMN circuit_open_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_routes ADD COLUMN stable_success_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_routes ADD COLUMN circuit_closed_at TEXT;
ALTER TABLE model_routes ADD COLUMN private_only INTEGER NOT NULL DEFAULT 0;
ALTER TABLE scheduler_config ADD COLUMN privacy_mode TEXT NOT NULL DEFAULT 'high';
ALTER TABLE audit_logs ADD COLUMN requested_model TEXT;
`

type Alias struct {
	ID            string
	Alias         string
	TargetModels  []string
	Description   string
	CreatedAt     string
}

// KaggleState mirrors the kaggle_refresh_state table (Node db.ts).
type KaggleState struct {
	ProviderKeyID       string
	ExpiresAt           string
	LastRefreshAt       string
	LastError           string
	ConsecutiveFailures int
}

type ClientToken struct {
	ID           string
	Name         string
	Token        string
	CreatedAt    string
	LastUsedAt   string
	Active       bool
	QuotaLimit   *int
	UsedTokens   int
	RequestCount int
	Note         string
}

type AuditRow struct {
	RequestID             string
	Timestamp             string
	SessionKey            string
	ClientTokenID         string
	ClientTokenName       string
	DetectedTaskType      string
	IsPrivate             bool
	SelectedProviderKeyID string
	SelectedRouteID       string
	SelectedModelID       string
	FailoverChainJSON     string
	EstimatedTokens       int
	BilledTokens          int
	UsageSource           string
	LatencyMs             int
	StatusCode            int
	ValidatorResult       string
	PreviewPrompt         string
	RequestedModel        string
}

// Store is the gateway's data access interface. MemStore implements
// it in memory (tests, and running without a DB file); SQLiteStore
// implements it against the Node-compatible schema.
type Store interface {
	ListRoutes() ([]scheduler.Route, error)
	ListKeys() ([]scheduler.ProviderKey, error)
	ListAliases() ([]Alias, error)
	GetAlias(name string) (*Alias, error)
	ListTokens() ([]ClientToken, error)
	GetTokenByValue(value string) (*ClientToken, error)
	GetConfig() (scheduler.SchedulerConfig, error)
	UpdateConfig(patch map[string]interface{}) (scheduler.SchedulerConfig, error)
	GetMetrics(routeID string) (scheduler.Metrics, error)
	GetQuota(keyID string) (scheduler.Quota, error)
	GetAffinity(sessionKey string) (routeID, keyID string, ok bool, err error)
	SetAffinity(sessionKey, routeID, keyID string, ttlSeconds int) error
	RecordSuccess(routeID, keyID string, billed, fallback, latencyMs int) error
	RecordFailure(routeID, keyID, errorCode, errorMsg string, latencyMs int, infra bool) error
	UpdateKeyState(keyID, state, cooldownUntil string, inc429 bool) error
	UpdateRouteState(routeID, state, unavailableUntil string) error
	UpdateCircuit(routeID, state, opensUntil string, timeouts, fivexx int) error
	AddAudit(row AuditRow) error
	ListAudit(limit, offset int) ([]AuditRow, error)
	UpsertKey(k scheduler.ProviderKey) error
	DeleteKey(id string) error
	UpsertRoute(r scheduler.Route) error
	DeleteRoute(id string) error
	UpsertToken(t ClientToken) error
	DeleteToken(id string) error
	UpsertAlias(a Alias) error
	DeleteAlias(id string) error
	ResetKey429(keyID string) error
	IncrementTokenUsage(tokenID string, billed int) error
	GetKeyByName(name string) (scheduler.ProviderKey, bool, error)
	GetKaggleState(providerKeyID string) (KaggleState, bool, error)
	UpsertKaggleState(state KaggleState) error
	UpdateKaggleKey(providerKeyID, encryptedKey, keyMask, expiresAt, lastRefreshAt string) error
}

// ---- MemStore ----

type MemStore struct {
	mu      sync.Mutex
	routes  map[string]scheduler.Route
	keys    map[string]scheduler.ProviderKey
	aliases map[string]Alias // by alias name
	tokens  map[string]ClientToken
	config  scheduler.SchedulerConfig
	metrics map[string]scheduler.Metrics
	quotas  map[string]scheduler.Quota
	kaggle  map[string]KaggleState
	aff     map[string]struct {
		route, key string
		exp        time.Time
	}
	audit []AuditRow
}

func NewMemStore() *MemStore {
	return &MemStore{
		routes: map[string]scheduler.Route{}, keys: map[string]scheduler.ProviderKey{},
		aliases: map[string]Alias{}, tokens: map[string]ClientToken{},
		config: scheduler.SchedulerConfig{DispatchMode: "auto", WeightsJSON: `{"wSticky":0.35,"wRole":0.30,"wRel":0.20,"wCost":0.10,"wLat":0.05}`, RateLimitRPM: 120, PrivacyMode: "off"},
		metrics: map[string]scheduler.Metrics{}, quotas: map[string]scheduler.Quota{},
		kaggle: map[string]KaggleState{},
		aff: map[string]struct {
			route, key string
			exp        time.Time
		}{},
	}
}

func (m *MemStore) ListRoutes() ([]scheduler.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []scheduler.Route{}
	for _, r := range m.routes {
		out = append(out, r)
	}
	return out, nil
}
func (m *MemStore) ListKeys() ([]scheduler.ProviderKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []scheduler.ProviderKey{}
	for _, k := range m.keys {
		out = append(out, k)
	}
	return out, nil
}
func (m *MemStore) ListAliases() ([]Alias, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Alias{}
	for _, a := range m.aliases {
		out = append(out, a)
	}
	return out, nil
}
func (m *MemStore) GetAlias(name string) (*Alias, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.aliases[name]
	if !ok {
		return nil, nil
	}
	return &a, nil
}
func (m *MemStore) ListTokens() ([]ClientToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ClientToken{}
	for _, t := range m.tokens {
		out = append(out, t)
	}
	return out, nil
}
func (m *MemStore) GetTokenByValue(v string) (*ClientToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.Token == v {
			c := t
			return &c, nil
		}
	}
	return nil, nil
}
func (m *MemStore) GetConfig() (scheduler.SchedulerConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config, nil
}
func (m *MemStore) UpdateConfig(patch map[string]interface{}) (scheduler.SchedulerConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := patch["privacyMode"].(string); ok {
		m.config.PrivacyMode = v
	}
	if v, ok := patch["dispatchMode"].(string); ok {
		m.config.DispatchMode = v
	}
	if v, ok := patch["pinnedModelId"].(string); ok {
		m.config.PinnedModelID = v
	}
	if v, ok := patch["weightsJson"].(string); ok {
		m.config.WeightsJSON = v
	}
	if v, ok := patch["rateLimitRpm"].(float64); ok {
		m.config.RateLimitRPM = int(v)
	}
	return m.config, nil
}
func (m *MemStore) GetMetrics(routeID string) (scheduler.Metrics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.metrics[routeID]
	if !ok {
		return scheduler.Metrics{LatencyP50: 450, LatencyP95: 1200}, nil
	}
	return mt, nil
}
func (m *MemStore) GetQuota(keyID string) (scheduler.Quota, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q, ok := m.quotas[keyID]
	if !ok {
		return scheduler.Quota{}, nil
	}
	return q, nil
}
func (m *MemStore) GetAffinity(k string) (string, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.aff[k]
	if !ok || time.Now().After(a.exp) {
		return "", "", false, nil
	}
	return a.route, a.key, true, nil
}
func (m *MemStore) SetAffinity(k, routeID, keyID string, ttl int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aff[k] = struct {
		route, key string
		exp        time.Time
	}{routeID, keyID, time.Now().Add(time.Duration(ttl) * time.Second)}
	return nil
}
func (m *MemStore) RecordSuccess(routeID, keyID string, billed, fallback, latency int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mt := m.metrics[routeID]
	mt.SuccessCount++
	mt.LatencyP50 = latency
	m.metrics[routeID] = mt
	q := m.quotas[keyID]
	q.BilledTokensToday += billed
	q.FallbackTokensToday += fallback
	// FallbackTokensToday is not in scheduler.Quota; it is tracked in SQLite only.
	m.quotas[keyID] = q
	if r, ok := m.routes[routeID]; ok {
		r.ConsecutiveFailures = 0
		r.CircuitState = "closed"
		m.routes[routeID] = r
	}
	if k, ok := m.keys[keyID]; ok {
		k.Consecutive429 = 0
		m.keys[keyID] = k
	}
	return nil
}
func (m *MemStore) RecordFailure(routeID, keyID, code, msg string, latency int, infra bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mt := m.metrics[routeID]
	mt.FailureCount++
	mt.LastErrorCode = code
	m.metrics[routeID] = mt
	if infra {
		if r, ok := m.routes[routeID]; ok {
			r.ConsecutiveFailures++
			if r.ConsecutiveFailures >= 3 {
				r.CircuitState = "open"
				r.CircuitOpenCount++
				// Backoff: min(60*2^(oldOpenCount), 900) seconds.
				secs := 60
				for i := 1; i < r.CircuitOpenCount; i++ {
					secs *= 2
					if secs >= 900 {
						secs = 900
						break
					}
				}
				if secs > 900 {
					secs = 900
				}
				r.CircuitOpensUntil = time.Now().Add(time.Duration(secs) * time.Second).Format(time.RFC3339)
			}
			m.routes[routeID] = r
		}
	}
	return nil
}
func (m *MemStore) UpdateKeyState(keyID, state, cooldown string, inc429 bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.keys[keyID]; ok {
		k.KeyState = state
		k.CooldownUntil = cooldown
		if inc429 {
			k.Consecutive429++
		}
		m.keys[keyID] = k
	}
	return nil
}
func (m *MemStore) UpdateRouteState(routeID, state, until string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.routes[routeID]; ok {
		r.RouteState = state
		r.UnavailableUntil = until
		m.routes[routeID] = r
	}
	return nil
}
func (m *MemStore) UpdateCircuit(routeID, state, opens string, timeouts, fivexx int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.routes[routeID]; ok {
		r.CircuitState = state
		r.CircuitOpensUntil = opens
		r.ConsecutiveTimeouts = timeouts
		r.Consecutive5xx = fivexx
		if state == "closed" {
			r.ConsecutiveFailures = 0
		}
		m.routes[routeID] = r
	}
	return nil
}
func (m *MemStore) AddAudit(row AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append([]AuditRow{row}, m.audit...)
	if len(m.audit) > 200 {
		m.audit = m.audit[:200]
	}
	return nil
}
func (m *MemStore) ListAudit(limit, offset int) ([]AuditRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if offset >= len(m.audit) {
		return []AuditRow{}, nil
	}
	end := offset + limit
	if end > len(m.audit) {
		end = len(m.audit)
	}
	return append([]AuditRow{}, m.audit[offset:end]...), nil
}
func (m *MemStore) UpsertKey(k scheduler.ProviderKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[k.ID] = k
	return nil
}
func (m *MemStore) DeleteKey(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, id)
	return nil
}
func (m *MemStore) UpsertRoute(r scheduler.Route) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[r.ID] = r
	return nil
}
func (m *MemStore) DeleteRoute(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.routes, id)
	return nil
}
func (m *MemStore) UpsertToken(t ClientToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[t.ID] = t
	return nil
}
func (m *MemStore) DeleteToken(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, id)
	return nil
}
func (m *MemStore) UpsertAlias(a Alias) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aliases[a.Alias] = a
	return nil
}
func (m *MemStore) DeleteAlias(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, a := range m.aliases {
		if a.ID == id {
			delete(m.aliases, name)
		}
	}
	return nil
}
func (m *MemStore) GetKeyByName(name string) (scheduler.ProviderKey, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.keys {
		if k.Name == name {
			return k, true, nil
		}
	}
	return scheduler.ProviderKey{}, false, nil
}
func (m *MemStore) GetKaggleState(providerKeyID string) (KaggleState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.kaggle[providerKeyID]
	return st, ok, nil
}
func (m *MemStore) UpsertKaggleState(state KaggleState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kaggle[state.ProviderKeyID] = state
	return nil
}

// UpdateKaggleKey mirrors Node's updateKaggleProviderKey: it encrypts
// (done by the caller), sets key_state='healthy', clears cooldown, and
// upserts the refresh state with lastError='' and failures=0. It does
// NOT touch quota or account notes.
func (m *MemStore) UpdateKaggleKey(providerKeyID, encryptedKey, keyMask, expiresAt, lastRefreshAt string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.keys[providerKeyID]; ok {
		k.EncryptedKey = encryptedKey
		k.KeyMask = keyMask
		k.KeyState = "healthy"
		k.CooldownUntil = ""
		m.keys[providerKeyID] = k
	}
	m.kaggle[providerKeyID] = KaggleState{
		ProviderKeyID: providerKeyID, ExpiresAt: expiresAt,
		LastRefreshAt: lastRefreshAt, LastError: "", ConsecutiveFailures: 0,
	}
	return nil
}
func (m *MemStore) ResetKey429(keyID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.keys[keyID]; ok {
		k.Consecutive429 = 0
		m.keys[keyID] = k
	}
	return nil
}
func (m *MemStore) IncrementTokenUsage(tokenID string, billed int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tokens[tokenID]; ok {
		t.UsedTokens += billed
		t.RequestCount++
		m.tokens[tokenID] = t
	}
	return nil
}

// ---- SQLiteStore ----

type SQLiteStore struct {
	db *sql.DB
}

func OpenSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path+"?cache=shared")
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA cache_size = -4000;",
		"PRAGMA temp_store = MEMORY;",
		"PRAGMA mmap_size = 0;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(Schema); err != nil {
		db.Close()
		return nil, err
	}
	for _, mig := range strings.Split(migrations, ";") {
		mig = strings.TrimSpace(mig)
		if mig == "" {
			continue
		}
		_, _ = db.Exec(mig) // idempotent: column already exists is ignored
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM scheduler_config WHERE id='default'").Scan(&count); err == nil && count == 0 {
		_, _ = db.Exec(`INSERT INTO scheduler_config (id, dispatch_mode, pinned_model_id, weights_json, rate_limit_rpm, ip_allowlist_json, loopback_only, updated_at) VALUES ('default','auto',NULL,'{"wSticky":0.35,"wRole":0.30,"wRel":0.20,"wCost":0.10,"wLat":0.05}',120,'[]',0,?)`, time.Now().Format(time.RFC3339))
	}
	return &SQLiteStore{db: db}, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func intToBool(v int) bool { return v != 0 }
