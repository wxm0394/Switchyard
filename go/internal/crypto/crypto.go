// Package crypto implements the Node Railways provider-key encryption
// format, verified against server/security.ts:
//
//	AES-256-GCM, IV 12 random bytes, output iv_hex:authTag_hex:ciphertext_hex
//
// Master key resolution order:
//  1. GATEWAY_MASTER_KEY env: 64-char hex -> raw 32 bytes, else SHA-256 of the string.
//  2. GATEWAY_MASTER_KEY_FILE (default /var/lib/railways/master.key):
//     64-char hex file content -> raw 32 bytes.
//  3. Otherwise: FAIL LOUDLY. Unlike Node, Go never generates a new key,
//     because a generated key would orphan every existing encrypted key.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// DefaultMasterKeyFile is the deployment path. Node's code default is
// .gateway-data/master.key relative to the server directory; the Go
// config makes the path explicit so a migration cannot silently pick
// the wrong file.
const DefaultMasterKeyFile = "/var/lib/railways/master.key"

// LoadMasterKey resolves the master key or returns an error.
// It never generates, writes, or logs a key.
func LoadMasterKey() ([]byte, error) {
	if env := strings.TrimSpace(os.Getenv("GATEWAY_MASTER_KEY")); env != "" {
		if len(env) == 64 {
			b, err := hex.DecodeString(env)
			if err == nil && len(b) == 32 {
				return b, nil
			}
		}
		sum := sha256.Sum256([]byte(env))
		return sum[:], nil
	}
	path := strings.TrimSpace(os.Getenv("GATEWAY_MASTER_KEY_FILE"))
	if path == "" {
		path = DefaultMasterKeyFile
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("switchyard crypto: no GATEWAY_MASTER_KEY and cannot read master key file %s: %w (refusing to generate a new key; existing encrypted keys would be orphaned)", path, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 64 {
		b, err := hex.DecodeString(trimmed)
		if err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("switchyard crypto: master key file %s is not 64-char hex", path)
}

// Encrypt encrypts plaintext with a fresh 12-byte IV.
func Encrypt(key []byte, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if len(key) != 32 {
		return "", errors.New("switchyard crypto: master key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil)
	// GCM sealed = ciphertext || authTag (16 bytes, appended by Go).
	ct, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	return hex.EncodeToString(iv) + ":" + hex.EncodeToString(tag) + ":" + hex.EncodeToString(ct), nil
}

// Decrypt decrypts a Node-format string. A string without exactly
// three colon-separated parts is returned unchanged (Node legacy
// unencrypted passthrough for reads). A malformed or tampered
// encrypted value returns an error; callers must not silently
// treat it as empty except where display masking explicitly does so.
func Decrypt(key []byte, encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}
	parts := strings.Split(encrypted, ":")
	if len(parts) != 3 {
		return encrypted, nil
	}
	if len(key) != 32 {
		return "", errors.New("switchyard crypto: master key must be 32 bytes")
	}
	iv, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("switchyard crypto: bad iv hex: %w", err)
	}
	tag, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("switchyard crypto: bad auth tag hex: %w", err)
	}
	ct, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("switchyard crypto: bad ciphertext hex: %w", err)
	}
	if len(iv) != 12 {
		return "", fmt.Errorf("switchyard crypto: iv must be 12 bytes, got %d", len(iv))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	sealed := append(append([]byte{}, ct...), tag...)
	plain, err := gcm.Open(nil, iv, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("switchyard crypto: authentication failed: %w", err)
	}
	return string(plain), nil
}

// Mask formats a key mask exactly like Node maskApiKey.
func Mask(raw string) string {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return "未配置密钥"
	}
	if len(clean) <= 8 {
		return "••••••"
	}
	if strings.HasPrefix(clean, "sk-") {
		return "sk-••••" + clean[len(clean)-4:]
	}
	return clean[:4] + "••••" + clean[len(clean)-4:]
}
