// Package kaggle ports server/kaggle-refresh.ts (Railways, built-in
// component, not an external cron) exactly.
//
// Positioning, from Node: this is a component inside the same process,
// same DB, same config as /v1/chat/completions. It adds no process and
// no external cron. Clients change nothing: URL, client token, aliases
// are untouched. The timer only swaps the Kaggle provider key in
// SQLite; the next request re-reads it (per-request DB re-read).
//
// Safety: this file contains no real key, token, or encrypted blob.
// Kaggle credentials come from the environment only. Logs never print
// a full key, only expiry time, provider key id, and success/failure.
//
// Default is OFF: KAGGLE_REFRESH_ENABLED must be exactly "true" for the
// timer to start.
//
// Issuance (verified against Kaggle's official source in Node comments,
// not guessed): POST
// https://api.kaggle.com/v1/models.ModelProxyApiService/CreateDefaultModelProxyToken
// with body {}, Bearer auth with the website API token (KAGGLE_API_KEY).
// KAGGLE_USERNAME is an identifier only, never used for auth. The
// 2h TTL in SDK comments is NOT used as a fixed interval: remaining
// life always comes from the server-returned expiryTime.
//
// Single-active-token caveat, from Node: re-calling issuance before
// expiry may return the same cached token. That case returns action
// "cached": it is neither success nor failure, does not overwrite the
// DB, and the timer loop retries soon after expiry.
package kaggle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
)

// Config mirrors Node's KaggleRefreshConfig.
type Config struct {
	Enabled              bool
	ProviderName         string
	ModelID              string
	CheckSeconds         int
	RefreshBeforeSeconds int
}

// ConfigFromEnv mirrors Node's getKaggleRefreshConfig exactly:
// enabled only when the env value is exactly "true"; check/refresh
// intervals fall back to 600/1200 when missing, non-finite, or <= 0;
// model id defaults to google/gemini-3-flash-preview.
func ConfigFromEnv() Config {
	check := parsePositiveSeconds(os.Getenv("KAGGLE_REFRESH_CHECK_SECONDS"), 600)
	before := parsePositiveSeconds(os.Getenv("KAGGLE_REFRESH_BEFORE_SECONDS"), 1200)
	modelID := "google/gemini-3-flash-preview"
	if v, ok := os.LookupEnv("KAGGLE_MODEL_ID"); ok {
		modelID = strings.TrimSpace(v)
	}
	return Config{
		Enabled:              os.Getenv("KAGGLE_REFRESH_ENABLED") == "true",
		ProviderName:         strings.TrimSpace(os.Getenv("KAGGLE_PROVIDER_NAME")),
		ModelID:              modelID,
		CheckSeconds:         check,
		RefreshBeforeSeconds: before,
	}
}

func parsePositiveSeconds(raw string, fallback int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || f <= 0 || f != f || f > 1e18 {
		return fallback
	}
	return int(f)
}

// ParseExpiryToEpoch mirrors Node's parseExpiryToEpoch: trimmed string;
// all-digits: length >= 13 means milliseconds, else seconds; otherwise
// a date string parsed to seconds. Returns false when unparseable.
func ParseExpiryToEpoch(expiresAt string) (int64, bool) {
	raw := strings.TrimSpace(expiresAt)
	if raw == "" {
		return 0, false
	}
	isDigits := true
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			isDigits = false
			break
		}
	}
	if isDigits {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, false
		}
		if len(raw) >= 13 {
			return n / 1000, true
		}
		return n, true
	}
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		time.ANSIC, time.RFC1123, time.RFC1123Z,
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// RefreshedKey is what an issuer returns: the new raw key, its expiry
// string as returned by Kaggle (may be empty), and the base URI Kaggle
// reports (may be empty).
type RefreshedKey struct {
	APIKey    string
	ExpiresAt string
	BaseURI   string
}

// Issuer issues a fresh Model Proxy key. Implementations must never
// log the returned key.
type Issuer interface {
	Refresh(ctx context.Context) (RefreshedKey, error)
}

// HTTP Issuer is the real Kaggle issuer. APIKey is the website API
// token (KAGGLE_API_KEY); Username is an identifier only and is never
// sent. BaseURL defaults to the Kaggle issuance endpoint and is
// overridable only for tests against a mock server.
type HTTPIssuer struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (h *HTTPIssuer) endpoint() string {
	if strings.TrimSpace(h.BaseURL) != "" {
		return strings.TrimSpace(h.BaseURL)
	}
	return "https://api.kaggle.com/v1/models.ModelProxyApiService/CreateDefaultModelProxyToken"
}

// Refresh mirrors Node's kaggleHttpKeyProvider: POST {}, Bearer auth,
// non-OK -> "Kaggle 签发失败 HTTP <status>: <body first 200 chars>",
// missing/non-string token -> "Kaggle 签发响应里没有 token 字段，旧 Key 原样保留".
// Response fields are read from the top level, falling back to the
// nested result ?? data object, exactly as in Node.
func (h *HTTPIssuer) Refresh(ctx context.Context) (RefreshedKey, error) {
	apiKey := strings.TrimSpace(h.APIKey)
	if apiKey == "" {
		return RefreshedKey{}, fmt.Errorf("Kaggle 凭证未配置")
	}
	client := h.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint(), bytes.NewReader([]byte("{}")))
	if err != nil {
		return RefreshedKey{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return RefreshedKey{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := string(body)
		if len(text) > 200 {
			text = text[:200]
		}
		return RefreshedKey{}, fmt.Errorf("Kaggle 签发失败 HTTP %d: %s", resp.StatusCode, text)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return RefreshedKey{}, fmt.Errorf("Kaggle 签发响应里没有 token 字段，旧 Key 原样保留")
	}
	nested := map[string]interface{}{}
	if v, ok := data["result"].(map[string]interface{}); ok {
		nested = v
	} else if v, ok := data["data"].(map[string]interface{}); ok {
		nested = v
	}
	strField := func(m map[string]interface{}, key string) string {
		if s, ok := m[key].(string); ok {
			return s
		}
		return ""
	}
	token := strField(data, "token")
	if token == "" {
		token = strField(nested, "token")
	}
	if token == "" {
		return RefreshedKey{}, fmt.Errorf("Kaggle 签发响应里没有 token 字段，旧 Key 原样保留")
	}
	expiresAt := strField(data, "expiryTime")
	if expiresAt == "" {
		expiresAt = strField(nested, "expiryTime")
	}
	baseURI := strField(data, "baseUri")
	if baseURI == "" {
		baseURI = strField(nested, "baseUri")
	}
	return RefreshedKey{APIKey: token, ExpiresAt: expiresAt, BaseURI: baseURI}, nil
}

// Store is the narrow store surface the refresher needs. store.Store
// satisfies it once the Kaggle methods are added there.
type Store interface {
	GetKeyByName(name string) (scheduler.ProviderKey, bool, error)
	GetKaggleState(providerKeyID string) (store.KaggleState, bool, error)
	UpsertKaggleState(state store.KaggleState) error
	UpdateKaggleKey(providerKeyID, encryptedKey, keyMask, expiresAt, lastRefreshAt string) error
}

// Status mirrors Node's getKaggleRefreshStatus.
type Status struct {
	Enabled             bool   `json:"enabled"`
	ProviderName        string `json:"providerName"`
	ModelID             string `json:"modelId"`
	ProviderFound       bool   `json:"providerFound"`
	KeyState            string `json:"keyState"`
	ExpiresAt           string `json:"expiresAt"`
	RemainingSeconds    *int64 `json:"remainingSeconds"`
	LastRefreshAt       string `json:"lastRefreshAt"`
	LastError           string `json:"lastError"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
}

// Result mirrors Node's runKaggleRefreshOnce return value.
type Result struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// Refresher runs one refresh and the timer loop. Decrypt converts a
// stored encrypted key back to raw form (crypto.Decrypt with the
// gateway master key); a decrypt failure never blocks the refresh
// path, exactly as in Node. Encrypt converts a raw key to the stored
// encrypted form (crypto.Encrypt); Mask formats it like crypto.Mask.
type Refresher struct {
	Config  Config
	Store   Store
	Issuer  Issuer
	Decrypt func(encrypted string) (string, error)
	Encrypt func(raw string) (string, error)
	Mask    func(raw string) string
	Now     func() time.Time // nil means time.Now; tests may override.

	refreshing atomic.Bool
	running    atomic.Bool
}

func (r *Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Status reads the current state without issuing anything.
func (r *Refresher) Status() (Status, error) {
	st := Status{
		Enabled:      r.Config.Enabled,
		ProviderName: r.Config.ProviderName,
		ModelID:      r.Config.ModelID,
	}
	if r.Config.ProviderName == "" {
		return st, nil
	}
	key, found, err := r.Store.GetKeyByName(r.Config.ProviderName)
	if err != nil {
		return st, err
	}
	if !found {
		return st, nil
	}
	st.ProviderFound = true
	st.KeyState = key.KeyState
	state, ok, err := r.Store.GetKaggleState(key.ID)
	if err != nil {
		return st, err
	}
	if !ok {
		return st, nil
	}
	st.ExpiresAt = state.ExpiresAt
	if epoch, ok := ParseExpiryToEpoch(state.ExpiresAt); ok {
		remaining := epoch - r.now().Unix()
		st.RemainingSeconds = &remaining
	}
	st.LastRefreshAt = state.LastRefreshAt
	st.LastError = state.LastError
	st.ConsecutiveFailures = state.ConsecutiveFailures
	return st, nil
}

// RunOnce mirrors Node's runKaggleRefreshOnce step for step, including
// its order: provider-name check, key-row check, remaining-time check,
// in-progress check, cached-token check, base-URI note, key update,
// and failure state recording. It never overwrites the stored key on
// failure or on a cached token. Store errors on the lookup path are
// returned to the caller; refresh failures are returned as
// Result{Action: "failed"} after recording state, as in Node.
func (r *Refresher) RunOnce(ctx context.Context) (Result, error) {
	if r.Config.ProviderName == "" {
		return Result{Action: "skipped", Reason: "KAGGLE_PROVIDER_NAME 未设置"}, nil
	}
	keyRow, found, err := r.Store.GetKeyByName(r.Config.ProviderName)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{Action: "skipped", Reason: "galbine 里找不到名为 " + r.Config.ProviderName + " 的 Key，不另起新 Key"}, nil
	}
	state, _, err := r.Store.GetKaggleState(keyRow.ID)
	if err != nil {
		return Result{}, err
	}
	nowEpoch := r.now().Unix()
	if epoch, ok := ParseExpiryToEpoch(state.ExpiresAt); ok {
		if epoch-nowEpoch > int64(r.Config.RefreshBeforeSeconds) {
			return Result{Action: "skipped", Reason: fmt.Sprintf("现有 Key 还剩 %ds，未到换 Key 阈值", epoch-nowEpoch)}, nil
		}
	}
	if !r.refreshing.CompareAndSwap(false, true) {
		return Result{Action: "skipped", Reason: "上一次换 Key 还没跑完，跳过本次检查"}, nil
	}
	defer r.refreshing.Store(false)

	fail := func(message string) (Result, error) {
		prev, _, err := r.Store.GetKaggleState(keyRow.ID)
		if err != nil {
			return Result{}, err
		}
		if err := r.Store.UpsertKaggleState(store.KaggleState{
			ProviderKeyID:       keyRow.ID,
			ExpiresAt:           prev.ExpiresAt,
			LastRefreshAt:       prev.LastRefreshAt,
			LastError:           message,
			ConsecutiveFailures: prev.ConsecutiveFailures + 1,
		}); err != nil {
			return Result{}, err
		}
		log.Printf("[KAGGLE REFRESH] 换 Key 失败，旧 Key 原样保留：%s", message)
		return Result{Action: "failed", Reason: message}, nil
	}

	fresh, err := r.Issuer.Refresh(ctx)
	if err != nil {
		return fail(err.Error())
	}
	if strings.TrimSpace(fresh.APIKey) == "" {
		return fail("签发返回的 Key 为空，旧 Key 原样保留")
	}
	// Cached-token correction: same raw key + same expiry time means
	// Kaggle returned its single active cached token, not a new one.
	// Not a success, not a failure, do not overwrite the DB.
	if r.Decrypt != nil {
		if currentRaw, err := r.Decrypt(keyRow.EncryptedKey); err == nil {
			if currentRaw != "" && strings.TrimSpace(fresh.APIKey) == currentRaw &&
				fresh.ExpiresAt != "" && state.ExpiresAt != "" && fresh.ExpiresAt == state.ExpiresAt {
				return Result{Action: "cached", Reason: "Kaggle 返回同一枚缓存 token，到期时间未变，过期后再试"}, nil
			}
		}
		// Decrypt failure does not block the refresh path, as in Node.
	}
	if fresh.BaseURI != "" && fresh.BaseURI != "https://mp-staging.kaggle.net/models/openapi" {
		log.Printf("[KAGGLE REFRESH] 注意：签发返回的 baseUri 与推理地址不一致，暂不自动改路由：%s", fresh.BaseURI)
	}
	encrypted, err := r.Encrypt(strings.TrimSpace(fresh.APIKey))
	if err != nil {
		return fail(err.Error())
	}
	mask := ""
	if r.Mask != nil {
		mask = r.Mask(strings.TrimSpace(fresh.APIKey))
	}
	if err := r.Store.UpdateKaggleKey(keyRow.ID, encrypted, mask, fresh.ExpiresAt, r.now().UTC().Format(time.RFC3339)); err != nil {
		return Result{}, err
	}
	log.Printf("[KAGGLE REFRESH] 已更新 providerKeyId=%s，新到期时间=%s", keyRow.ID, fresh.ExpiresAt)
	return Result{Action: "refreshed", Reason: "已换 Key 并复活为 healthy"}, nil
}

// Start runs the timer loop in the background, mirroring Node's
// startKaggleRefreshTimer: it returns immediately (no goroutine) when
// disabled, runs once immediately on startup instead of waiting for
// the first period, and uses a recursive dynamic delay instead of a
// fixed ticker. The loop stops when ctx is cancelled. A status-read
// error inside the loop falls back to the default delay and continues;
// unlike Node's setTimeout chain, a transient status error must not
// silently kill the timer.
func (r *Refresher) Start(ctx context.Context) {
	if !r.Config.Enabled {
		log.Printf("[KAGGLE REFRESH] 未开启（KAGGLE_REFRESH_ENABLED 非 true），定时器不启动")
		return
	}
	if !r.running.CompareAndSwap(false, true) {
		return
	}
	log.Printf("[KAGGLE REFRESH] 组件已启动：每 %ds 检查一次，剩不到 %ds 才换，provider=%s",
		r.Config.CheckSeconds, r.Config.RefreshBeforeSeconds, r.Config.ProviderName)
	go func() {
		defer r.running.Store(false)
		for {
			result, err := r.RunOnce(ctx)
			if err != nil {
				log.Printf("[KAGGLE REFRESH] 定时检查失败：%v", err)
				result = Result{Action: "failed", Reason: err.Error()}
			}
			delay := time.Duration(r.Config.CheckSeconds) * time.Second
			var remaining *int64
			if st, err := r.Status(); err == nil {
				remaining = st.RemainingSeconds
			} else {
				log.Printf("[KAGGLE REFRESH] 状态读取失败，使用默认间隔：%v", err)
			}
			if result.Action == "cached" && remaining != nil && *remaining <= 600 {
				delay = 60 * time.Second
			} else if remaining != nil && *remaining > int64(r.Config.RefreshBeforeSeconds) {
				if d := time.Duration(*remaining-int64(r.Config.RefreshBeforeSeconds)) * time.Second; d < delay {
					delay = d
				}
			}
			if delay < 10*time.Second {
				delay = 10 * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
