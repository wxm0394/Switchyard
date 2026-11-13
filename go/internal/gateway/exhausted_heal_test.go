package gateway

import (
	"testing"
	"time"

	"github.com/wxm0394/Switchyard/internal/crypto"
	"github.com/wxm0394/Switchyard/internal/scheduler"
	"github.com/wxm0394/Switchyard/internal/store"
)

// Gateway with a single key in the given state and one healthy route.
// Mirrors testGateway but lets the caller set the key state.
func testGatewayWithKey(t *testing.T, keyState, cooldownUntil string, consec429 int) (*Gateway, *store.MemStore, scheduler.Route, scheduler.ProviderKey) {
	t.Helper()
	mem := store.NewMemStore()
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 7)
	}
	enc, err := crypto.Encrypt(keyBytes, "upstream-test-key")
	if err != nil {
		t.Fatal(err)
	}
	key := scheduler.ProviderKey{ID: "k1", Name: "Test", ProviderType: "openai-compatible", EncryptedKey: enc, KeyMask: "test••••key", KeyState: keyState, CooldownUntil: cooldownUntil, Consecutive429: consec429, BaseURL: "http://example.invalid/v1"}
	_ = mem.UpsertKey(key)
	route := scheduler.Route{ID: "r1", Name: "Test route", ProviderKeyID: "k1", ModelID: "test-model", DisplayName: "test-model", BaseURL: "http://example.invalid/v1", ProviderType: "openai-compatible", UpstreamModel: "test-model", TaskRole: "auto", RouteState: "healthy", CircuitState: "closed", SupportsVision: true, SupportsTools: true, SupportsJSONMode: true, SupportsStreaming: true, MaxContext: 128000, ReliabilityScore: 100, CostWeight: 1, Enabled: true}
	_ = mem.UpsertRoute(route)
	gw := New(mem, Config{MasterKey: keyBytes, Decrypt: func(enc string) (string, error) { return crypto.Decrypt(keyBytes, enc) }})
	return gw, mem, route, key
}

func readKey(t *testing.T, mem *store.MemStore, id string) scheduler.ProviderKey {
	t.Helper()
	keys, err := mem.ListKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.ID == id {
			return k
		}
	}
	t.Fatalf("key %s not found", id)
	return scheduler.ProviderKey{}
}

func TestEffectiveStateExhaustedHeal(t *testing.T) {
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	cases := []struct {
		name      string
		keyState  string
		cooldown  string
		want      string
		wantStore string // expected stored key_state after the call
		wantCool  string // expected stored cooldown_until after the call
	}{
		{"exhausted expired cooldown heals", "exhausted", past, "healthy", "healthy", ""},
		{"exhausted no cooldown heals (NULL date treated as expired)", "exhausted", "", "healthy", "healthy", ""},
		{"exhausted future cooldown stays exhausted", "exhausted", future, "exhausted", "exhausted", future},
		{"exhausted unparsable cooldown stays exhausted", "exhausted", "not-a-date", "exhausted", "exhausted", "not-a-date"},
		{"cooldown expired still heals (regression guard)", "cooldown", past, "healthy", "healthy", ""},
		{"cooldown no cooldown heals (NULL date treated as expired)", "cooldown", "", "healthy", "healthy", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw, mem, route, key := testGatewayWithKey(t, tc.keyState, tc.cooldown, 3)
			got := gw.effectiveStateWithHeal(route, &key)
			if got != tc.want {
				t.Fatalf("effectiveStateWithHeal = %q, want %q", got, tc.want)
			}
			stored := readKey(t, mem, "k1")
			if stored.KeyState != tc.wantStore || stored.CooldownUntil != tc.wantCool {
				t.Fatalf("stored key = (%q, %q), want (%q, %q)", stored.KeyState, stored.CooldownUntil, tc.wantStore, tc.wantCool)
			}
			if stored.Consecutive429 != 3 {
				t.Fatalf("consecutive_429 = %d, want 3 (heal must not touch it, like the reset endpoint)", stored.Consecutive429)
			}
		})
	}
}
