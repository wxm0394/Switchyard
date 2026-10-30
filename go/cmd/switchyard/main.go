// Command switchyard runs the Go Switchyard gateway.
//
// Environment:
//   SWITCHYARD_PORT (default 3000), SWITCHYARD_HOST (default 0.0.0.0)
//   SWITCHYARD_DB_PATH or GATEWAY_DB_PATH (default switchyard.db in cwd;
//     use :memory: only for smoke tests, never for production)
//   GATEWAY_MASTER_KEY (64-char hex, else SHA-256 of the string)
//   GATEWAY_MASTER_KEY_FILE (default /var/lib/railways/master.key)
//   GATEWAY_ADMIN_PASSWORD, NODE_ENV, GATEWAY_ALLOW_NO_ADMIN_AUTH,
//   GATEWAY_ADMIN_ORIGINS (comma-separated), GATEWAY_ALLOWED_HOSTS
//     (comma-separated)
//
// Master-key rule (see GO_API_CONTRACT.md §7): if neither
// GATEWAY_MASTER_KEY nor the master-key file exists, the server
// FAILS startup loudly and never generates a new key, because a
// generated key would orphan every existing encrypted provider key.
// Run with SWITCHYARD_ALLOW_NO_MASTER_KEY=1 only for a fresh,
// empty database with no migrated keys (the flag is refused when
// the DB already contains provider_keys rows).
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/wxm0394/Switchyard/internal/crypto"
	"github.com/wxm0394/Switchyard/internal/gateway"
	"github.com/wxm0394/Switchyard/internal/store"
)

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := []string{}
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func main() {
	port := envOr("SWITCHYARD_PORT", "3000")
	host := envOr("SWITCHYARD_HOST", "0.0.0.0")
	dbPath := envOr("SWITCHYARD_DB_PATH", envOr("GATEWAY_DB_PATH", "switchyard.db"))

	masterKey, err := crypto.LoadMasterKey()
	if err != nil {
		if os.Getenv("SWITCHYARD_ALLOW_NO_MASTER_KEY") == "1" {
			log.Printf("WARNING: running without a master key (SWITCHYARD_ALLOW_NO_MASTER_KEY=1); provider keys cannot be decrypted or created")
			masterKey = nil
		} else {
			log.Fatalf("switchyard: %v", err)
		}
	}
	var st store.Store
	if dbPath == ":memory:" {
		st = store.NewMemStore()
	} else {
		sqlite, err := store.OpenSQLite(dbPath)
		if err != nil {
			log.Fatalf("switchyard: open DB %s: %v", dbPath, err)
		}
		st = sqlite
		// Refuse the no-master-key flag when migrated keys exist.
		if masterKey == nil {
			keys, _ := st.ListKeys()
			if len(keys) > 0 {
				log.Fatalf("switchyard: SWITCHYARD_ALLOW_NO_MASTER_KEY=1 refused: DB already contains %d provider keys", len(keys))
			}
		}
	}
	decrypt := func(enc string) (string, error) {
		if masterKey == nil {
			return "", fmt.Errorf("master key is not configured")
		}
		return crypto.Decrypt(masterKey, enc)
	}
	gw := gateway.New(st, gateway.Config{MasterKey: masterKey, Decrypt: decrypt})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", gw.HandleModels)
	mux.HandleFunc("/api/v1/models", gw.HandleModels)
	mux.HandleFunc("/v1/chat/completions", gw.HandleChat)
	mux.HandleFunc("/api/v1/chat/completions", gw.HandleChat)
	guard := gateway.AdminGuardConfig{
		Password:       strings.TrimSpace(os.Getenv("GATEWAY_ADMIN_PASSWORD")),
		Production:     os.Getenv("NODE_ENV") == "production",
		AllowNoAuth:    strings.ToLower(strings.TrimSpace(os.Getenv("GATEWAY_ALLOW_NO_ADMIN_AUTH"))) == "true",
		AllowedHosts:   splitCSV(os.Getenv("GATEWAY_ALLOWED_HOSTS")),
		AllowedOrigins: splitCSV(os.Getenv("GATEWAY_ADMIN_ORIGINS")),
	}
	gw.RegisterAdmin(mux, guard)
	// Client CORS for /v1/* (admin CORS is handled in RegisterAdmin).
	addr := host + ":" + port
	log.Printf("switchyard listening on %s (db %s)", addr, dbPath)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("switchyard: %v", err)
	}
}
