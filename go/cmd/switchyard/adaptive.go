package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/wxm0394/Multi-Armed-Bandit/bridge"
	"github.com/wxm0394/Switchyard/internal/gateway"
)

// adaptiveStatePath returns where the learned state is persisted, or "" when
// persistence is not possible (in-memory database and no explicit path).
func adaptiveStatePath(dbPath string, getenv func(string) string) string {
	if p := strings.TrimSpace(getenv("SWITCHYARD_ADAPTIVE_STATE_PATH")); p != "" {
		return p
	}
	if dbPath == ":memory:" || strings.TrimSpace(dbPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), "adaptive_state.json")
}

// setupAdaptive reads SWITCHYARD_ADAPTIVE_ROTATION (off|shadow|on, default
// off) and, unless it is off, enables adaptive rotation on gw. It never fails
// startup: unreadable or corrupt saved state just starts empty. The returned
// stop function saves the state one last time; it is nil when there is
// nothing to flush.
func setupAdaptive(gw *gateway.Gateway, dbPath string, getenv func(string) string, logf func(string, ...any)) (stop func()) {
	raw := strings.TrimSpace(getenv("SWITCHYARD_ADAPTIVE_ROTATION"))
	mode, ok := bridge.ParseMode(raw)
	if !ok {
		logf("adaptive: invalid SWITCHYARD_ADAPTIVE_ROTATION=%q, adaptive rotation stays off", raw)
	}
	if mode == bridge.Off {
		return nil
	}
	ctl := bridge.New(bridge.Options{Mode: mode, Logf: logf})
	gw.SetAdaptive(ctl)
	logf("adaptive: rotation mode=%s", mode)
	path := adaptiveStatePath(dbPath, getenv)
	if path == "" {
		logf("adaptive: no state path (in-memory database); learned state is lost on restart")
		return nil
	}
	st := bridge.FileStore(path)
	if err := ctl.Load(st); err != nil {
		logf("adaptive: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ctl.Run(ctx, st, 30*time.Second)
		close(done)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}
