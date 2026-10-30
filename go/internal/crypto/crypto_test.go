package crypto

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	enc, err := Encrypt(key, "test-provider-key-value")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(enc, ":")
	if len(parts) != 3 {
		t.Fatalf("expected 3 colon-separated parts, got %d: %q", len(parts), enc)
	}
	iv, err := hex.DecodeString(parts[0])
	if err != nil || len(iv) != 12 {
		t.Fatalf("iv must decode to 12 bytes, got %d err %v", len(iv), err)
	}
	tag, err := hex.DecodeString(parts[1])
	if err != nil || len(tag) != 16 {
		t.Fatalf("auth tag must decode to 16 bytes, got %d err %v", len(tag), err)
	}
	dec, err := Decrypt(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "test-provider-key-value" {
		t.Fatalf("roundtrip mismatch: %q", dec)
	}
	// Two encryptions of the same value must differ (fresh IV).
	enc2, _ := Encrypt(key, "test-provider-key-value")
	if enc2 == enc {
		t.Fatal("encryption is deterministic; IV must be fresh")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	key := make([]byte, 32)
	enc, _ := Encrypt(key, "secret")
	// Flip a hex digit in the ciphertext part.
	parts := strings.Split(enc, ":")
	ct := parts[2]
	if ct[0] == '0' {
		ct = "1" + ct[1:]
	} else {
		ct = "0" + ct[1:]
	}
	if _, err := Decrypt(key, parts[0]+":"+parts[1]+":"+ct); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}

func TestDecryptLegacyPassthrough(t *testing.T) {
	key := make([]byte, 32)
	got, err := Decrypt(key, "plain-legacy-value")
	if err != nil || got != "plain-legacy-value" {
		t.Fatalf("legacy passthrough failed: %q err %v", got, err)
	}
	got, err = Decrypt(key, "")
	if err != nil || got != "" {
		t.Fatalf("empty passthrough failed: %q err %v", got, err)
	}
}

func TestLoadMasterKeyEnvHexAndHash(t *testing.T) {
	t.Setenv("GATEWAY_MASTER_KEY", strings.Repeat("ab", 32))
	k, err := LoadMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(k) != 32 || hex.EncodeToString(k) != strings.Repeat("ab", 32) {
		t.Fatal("64-char hex env must decode raw")
	}
	t.Setenv("GATEWAY_MASTER_KEY", "not-hex-64-chars")
	k, err = LoadMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("not-hex-64-chars"))
	if hex.EncodeToString(k) != hex.EncodeToString(want[:]) {
		t.Fatal("non-hex env must be SHA-256 hashed")
	}
}

func TestLoadMasterKeyFileAndMissingFailsLoudly(t *testing.T) {
	f := t.TempDir() + "/master.key"
	if err := os.WriteFile(f, []byte(strings.Repeat("cd", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_MASTER_KEY", "")
	t.Setenv("GATEWAY_MASTER_KEY_FILE", f)
	k, err := LoadMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(k) != strings.Repeat("cd", 32) {
		t.Fatal("file hex key mismatch")
	}
	// Missing everywhere must fail, never generate.
	t.Setenv("GATEWAY_MASTER_KEY_FILE", t.TempDir()+"/does-not-exist.key")
	if _, err := LoadMasterKey(); err == nil {
		t.Fatal("missing master key must fail loudly, not generate a new key")
	} else if !strings.Contains(err.Error(), "refusing to generate") {
		t.Fatalf("missing-key error must say it refuses to generate, got: %v", err)
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":            "未配置密钥",
		"short":       "••••••",
		"sk-abc123456": "sk-••••3456",
		"abcdefghij":  "abcd••••ghij",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}
