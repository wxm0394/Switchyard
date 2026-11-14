package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wxm0394/Multi-Armed-Bandit/bridge"
	"github.com/wxm0394/Switchyard/internal/gateway"
	"github.com/wxm0394/Switchyard/internal/store"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

type logSink struct{ lines []string }

func (l *logSink) logf(f string, a ...any) { l.lines = append(l.lines, fmt.Sprintf(f, a...)) }
func (l *logSink) has(sub string) bool {
	for _, x := range l.lines {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}

func newGW() *gateway.Gateway { return gateway.New(store.NewMemStore(), gateway.Config{}) }

func TestSetupAdaptiveOffByDefaultAndOnInvalid(t *testing.T) {
	for name, v := range map[string]string{"unset": "", "off": "off", "OFF": "OFF", "invalid": "banana", "true": "true"} {
		gw, ls := newGW(), &logSink{}
		stop := setupAdaptive(gw, "switchyard.db", env(map[string]string{"SWITCHYARD_ADAPTIVE_ROTATION": v}), ls.logf)
		if stop != nil || gw.Adaptive != nil || gw.Rotator.WeightFn != nil {
			t.Errorf("%s: adaptive was enabled", name)
		}
		if (name == "invalid" || name == "true") && !ls.has("invalid SWITCHYARD_ADAPTIVE_ROTATION") {
			t.Errorf("%s: no warning logged: %v", name, ls.lines)
		}
		if (name == "unset" || name == "off") && len(ls.lines) != 0 {
			t.Errorf("%s: off must be silent, got %v", name, ls.lines)
		}
	}
}

func TestSetupAdaptiveModes(t *testing.T) {
	for mode, want := range map[string]bridge.Mode{"shadow": bridge.Shadow, "on": bridge.On} {
		gw, ls := newGW(), &logSink{}
		dir := t.TempDir()
		stop := setupAdaptive(gw, filepath.Join(dir, "s.db"), env(map[string]string{"SWITCHYARD_ADAPTIVE_ROTATION": mode}), ls.logf)
		if stop == nil || gw.Adaptive == nil || gw.Adaptive.Mode() != want || gw.Rotator.WeightFn == nil {
			t.Fatalf("%s: not wired (stop=%v adaptive=%v)", mode, stop != nil, gw.Adaptive)
		}
		stop()
	}
}

func TestAdaptiveStatePath(t *testing.T) {
	cases := []struct {
		db, override, want string
	}{
		{"switchyard.db", "", "adaptive_state.json"},
		{"/data/switchyard.db", "", "/data/adaptive_state.json"},
		{"/data/switchyard.db", "/x/custom.json", "/x/custom.json"},
		{":memory:", "", ""},
		{":memory:", "/x/custom.json", "/x/custom.json"},
		{"", "", ""},
	}
	for _, c := range cases {
		got := adaptiveStatePath(c.db, env(map[string]string{"SWITCHYARD_ADAPTIVE_STATE_PATH": c.override}))
		if got != c.want {
			t.Errorf("db=%q override=%q: got %q want %q", c.db, c.override, got, c.want)
		}
	}
}

func TestSetupAdaptiveInMemoryDBWarnsAndHasNothingToFlush(t *testing.T) {
	gw, ls := newGW(), &logSink{}
	stop := setupAdaptive(gw, ":memory:", env(map[string]string{"SWITCHYARD_ADAPTIVE_ROTATION": "on"}), ls.logf)
	if stop != nil || gw.Adaptive == nil {
		t.Fatalf("stop=%v adaptive=%v", stop != nil, gw.Adaptive)
	}
	if !ls.has("lost on restart") {
		t.Fatalf("no warning about missing persistence: %v", ls.lines)
	}
}

func TestSetupAdaptivePersistsAndRestores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	e := env(map[string]string{"SWITCHYARD_ADAPTIVE_ROTATION": "on", "SWITCHYARD_ADAPTIVE_STATE_PATH": path})
	gw1, ls := newGW(), &logSink{}
	stop := setupAdaptive(gw1, "switchyard.db", e, ls.logf)
	for i := 0; i < 5; i++ {
		gw1.Adaptive.Observe("r1", "small", false, "server_error", 0, false)
	}
	stop() // must perform the final save
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "r1") {
		t.Fatalf("state file missing or empty: %v %q", err, raw)
	}
	gw2 := newGW()
	stop2 := setupAdaptive(gw2, "switchyard.db", e, ls.logf)
	defer stop2()
	if n := len(gw2.Adaptive.Bandit().Snapshot().Arms); n == 0 {
		t.Fatal("state was not restored")
	}
}

func TestSetupAdaptiveCorruptStateNeverFailsStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{definitely not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	gw, ls := newGW(), &logSink{}
	stop := setupAdaptive(gw, "switchyard.db", env(map[string]string{"SWITCHYARD_ADAPTIVE_ROTATION": "on", "SWITCHYARD_ADAPTIVE_STATE_PATH": path}), ls.logf)
	defer stop()
	if !ls.has("corrupt") {
		t.Fatalf("corrupt state not reported: %v", ls.lines)
	}
	if gw.Adaptive == nil || len(gw.Adaptive.Bandit().Snapshot().Arms) != 0 {
		t.Fatal("controller should be running with empty state")
	}
}
