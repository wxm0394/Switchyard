package kaggle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
)

type fakeStore struct {
	keys   map[string]scheduler.ProviderKey
	states map[string]store.KaggleState
}

func newFakeStore() *fakeStore {
	return &fakeStore{keys: map[string]scheduler.ProviderKey{}, states: map[string]store.KaggleState{}}
}

func (f *fakeStore) GetKeyByName(name string) (scheduler.ProviderKey, bool, error) {
	for _, k := range f.keys {
		if k.Name == name {
			return k, true, nil
		}
	}
	return scheduler.ProviderKey{}, false, nil
}
func (f *fakeStore) GetKaggleState(id string) (store.KaggleState, bool, error) {
	st, ok := f.states[id]
	return st, ok, nil
}
func (f *fakeStore) UpsertKaggleState(st store.KaggleState) error {
	f.states[st.ProviderKeyID] = st
	return nil
}
func (f *fakeStore) UpdateKaggleKey(id, encrypted, mask, expiresAt, lastRefresh string) error {
	if k, ok := f.keys[id]; ok {
		k.EncryptedKey = encrypted
		k.KeyMask = mask
		k.KeyState = "healthy"
		k.CooldownUntil = ""
		f.keys[id] = k
	}
	f.states[id] = store.KaggleState{ProviderKeyID: id, ExpiresAt: expiresAt, LastRefreshAt: lastRefresh, LastError: "", ConsecutiveFailures: 0}
	return nil
}

type fakeIssuer struct {
	key       string
	expiresAt string
	baseURI   string
	err       error
	calls     int
}

func (f *fakeIssuer) Refresh(ctx context.Context) (RefreshedKey, error) {
	f.calls++
	if f.err != nil {
		return RefreshedKey{}, f.err
	}
	return RefreshedKey{APIKey: f.key, ExpiresAt: f.expiresAt, BaseURI: f.baseURI}, nil
}

func testRefresher(fs *fakeStore, iss Issuer, cfg Config) *Refresher {
	return &Refresher{
		Config:  cfg,
		Store:   fs,
		Issuer:  iss,
		Decrypt: func(enc string) (string, error) { return strings.TrimPrefix(enc, "enc:"), nil },
		Encrypt: func(raw string) (string, error) { return "enc:" + raw, nil },
		Mask:    func(raw string) string { return "masked" },
		Now:     func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
}

func TestParseExpiry(t *testing.T) {
	// All-digits seconds.
	if v, ok := ParseExpiryToEpoch("1700000000"); !ok || v != 1700000000 {
		t.Fatalf("seconds parse: %v %v", v, ok)
	}
	// All-digits milliseconds (13+ digits).
	if v, ok := ParseExpiryToEpoch("1700000000000"); !ok || v != 1700000000 {
		t.Fatalf("ms parse: %v %v", v, ok)
	}
	// RFC3339.
	if v, ok := ParseExpiryToEpoch("2023-11-14T22:13:20Z"); !ok || v != 1700000000 {
		t.Fatalf("rfc3339 parse: %v %v", v, ok)
	}
	if _, ok := ParseExpiryToEpoch("not a date"); ok {
		t.Fatalf("invalid expiry must not parse")
	}
	if _, ok := ParseExpiryToEpoch(""); ok {
		t.Fatalf("empty expiry must not parse")
	}
}

func TestConfigDefaults(t *testing.T) {
	t.Setenv("KAGGLE_REFRESH_ENABLED", "true")
	t.Setenv("KAGGLE_PROVIDER_NAME", "kaggle")
	t.Setenv("KAGGLE_REFRESH_CHECK_SECONDS", "abc")
	t.Setenv("KAGGLE_REFRESH_BEFORE_SECONDS", "0")
	cfg := ConfigFromEnv()
	if !cfg.Enabled || cfg.CheckSeconds != 600 || cfg.RefreshBeforeSeconds != 1200 {
		t.Fatalf("config defaults wrong: %+v", cfg)
	}
	if cfg.ModelID != "google/gemini-3-flash-preview" {
		t.Fatalf("model default wrong: %s", cfg.ModelID)
	}
	t.Setenv("KAGGLE_REFRESH_ENABLED", "yes")
	if ConfigFromEnv().Enabled {
		t.Fatalf("enabled must require exactly 'true'")
	}
}

func TestRunOnceSkippedNoProviderName(t *testing.T) {
	fs := newFakeStore()
	r := testRefresher(fs, &fakeIssuer{}, Config{ProviderName: "", CheckSeconds: 600, RefreshBeforeSeconds: 1200})
	res, err := r.RunOnce(context.Background())
	if err != nil || res.Action != "skipped" {
		t.Fatalf("expected skipped, got %+v err=%v", res, err)
	}
}

func TestRunOnceSkippedKeyNotFound(t *testing.T) {
	fs := newFakeStore()
	r := testRefresher(fs, &fakeIssuer{}, Config{ProviderName: "missing", CheckSeconds: 600, RefreshBeforeSeconds: 1200})
	res, err := r.RunOnce(context.Background())
	if err != nil || res.Action != "skipped" {
		t.Fatalf("expected skipped, got %+v err=%v", res, err)
	}
}

func TestRunOnceSkippedPlentyRemaining(t *testing.T) {
	fs := newFakeStore()
	fs.keys["k1"] = scheduler.ProviderKey{ID: "k1", Name: "kaggle", KeyState: "healthy", EncryptedKey: "enc:old-key"}
	fs.states["k1"] = store.KaggleState{ProviderKeyID: "k1", ExpiresAt: "1700003600"} // 3600s ahead
	iss := &fakeIssuer{key: "new-key"}
	r := testRefresher(fs, iss, Config{ProviderName: "kaggle", CheckSeconds: 600, RefreshBeforeSeconds: 1200})
	res, err := r.RunOnce(context.Background())
	if err != nil || res.Action != "skipped" || iss.calls != 0 {
		t.Fatalf("expected skipped without issuer call, got %+v calls=%d err=%v", res, iss.calls, err)
	}
}

func TestRunOnceRefreshed(t *testing.T) {
	fs := newFakeStore()
	fs.keys["k1"] = scheduler.ProviderKey{ID: "k1", Name: "kaggle", KeyState: "exhausted", EncryptedKey: "enc:old-key"}
	fs.states["k1"] = store.KaggleState{ProviderKeyID: "k1", ExpiresAt: "1700000600"} // 600s ahead, under threshold
	iss := &fakeIssuer{key: "new-key", expiresAt: "1700007200"}
	r := testRefresher(fs, iss, Config{ProviderName: "kaggle", CheckSeconds: 600, RefreshBeforeSeconds: 1200})
	res, err := r.RunOnce(context.Background())
	if err != nil || res.Action != "refreshed" {
		t.Fatalf("expected refreshed, got %+v err=%v", res, err)
	}
	if fs.keys["k1"].EncryptedKey != "enc:new-key" || fs.keys["k1"].KeyState != "healthy" {
		t.Fatalf("key not updated: %+v", fs.keys["k1"])
	}
	if fs.states["k1"].ExpiresAt != "1700007200" || fs.states["k1"].ConsecutiveFailures != 0 {
		t.Fatalf("state not reset: %+v", fs.states["k1"])
	}
}

func TestRunOnceCachedToken(t *testing.T) {
	fs := newFakeStore()
	fs.keys["k1"] = scheduler.ProviderKey{ID: "k1", Name: "kaggle", KeyState: "healthy", EncryptedKey: "enc:same-key"}
	fs.states["k1"] = store.KaggleState{ProviderKeyID: "k1", ExpiresAt: "1700000600"}
	iss := &fakeIssuer{key: "same-key", expiresAt: "1700000600"}
	r := testRefresher(fs, iss, Config{ProviderName: "kaggle", CheckSeconds: 600, RefreshBeforeSeconds: 1200})
	res, err := r.RunOnce(context.Background())
	if err != nil || res.Action != "cached" {
		t.Fatalf("expected cached, got %+v err=%v", res, err)
	}
	// DB must not be overwritten and must not count as failure.
	if fs.states["k1"].ConsecutiveFailures != 0 {
		t.Fatalf("cached must not count as failure: %+v", fs.states["k1"])
	}
}

func TestRunOnceFailureKeepsOldKey(t *testing.T) {
	fs := newFakeStore()
	fs.keys["k1"] = scheduler.ProviderKey{ID: "k1", Name: "kaggle", KeyState: "healthy", EncryptedKey: "enc:old-key"}
	fs.states["k1"] = store.KaggleState{ProviderKeyID: "k1", ExpiresAt: "1700000600", ConsecutiveFailures: 2}
	iss := &fakeIssuer{err: fmt.Errorf("Kaggle 签发失败 HTTP 500: boom")}
	r := testRefresher(fs, iss, Config{ProviderName: "kaggle", CheckSeconds: 600, RefreshBeforeSeconds: 1200})
	res, err := r.RunOnce(context.Background())
	if err != nil || res.Action != "failed" {
		t.Fatalf("expected failed, got %+v err=%v", res, err)
	}
	if fs.keys["k1"].EncryptedKey != "enc:old-key" {
		t.Fatalf("old key must be kept: %+v", fs.keys["k1"])
	}
	if fs.states["k1"].ConsecutiveFailures != 3 || fs.states["k1"].ExpiresAt != "1700000600" {
		t.Fatalf("failure state wrong: %+v", fs.states["k1"])
	}
}

func TestHTTPIssuerMockServer(t *testing.T) {
	// Mock Kaggle issuance server: verifies Bearer auth and {} body,
	// returns nested result fields like the real API.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-website-token" {
			w.WriteHeader(401)
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"result": map[string]interface{}{
				"token":      "test-proxy-key",
				"expiryTime": "2023-11-15T00:13:20Z",
				"baseUri":    "https://mp-staging.kaggle.net/models/openapi",
			},
		})
	}))
	defer srv.Close()
	iss := &HTTPIssuer{APIKey: "test-website-token", BaseURL: srv.URL}
	got, err := iss.Refresh(context.Background())
	if err != nil || got.APIKey != "test-proxy-key" || got.ExpiresAt != "2023-11-15T00:13:20Z" {
		t.Fatalf("mock issuer failed: %+v err=%v", got, err)
	}
}

func TestHTTPIssuerErrorTruncatesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(strings.Repeat("x", 500)))
	}))
	defer srv.Close()
	iss := &HTTPIssuer{APIKey: "test-website-token", BaseURL: srv.URL}
	_, err := iss.Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("expected HTTP 500 error, got %v", err)
	}
	if len(err.Error()) > len("Kaggle 签发失败 HTTP 500: ")+200+10 {
		t.Fatalf("error body must be truncated to 200 chars: len=%d", len(err.Error()))
	}
}

func TestHTTPIssuerMissingToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result": {}}`))
	}))
	defer srv.Close()
	iss := &HTTPIssuer{APIKey: "test-website-token", BaseURL: srv.URL}
	if _, err := iss.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "没有 token") {
		t.Fatalf("expected missing-token error, got %v", err)
	}
}
