package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
)

func TestPreferExactConfigurationSurvivesReload(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		exact bool
	}{
		{"legacy omitted", `{"pool":{"prefer_uids":["u1"],"prefer_mode":"only"}}`, false},
		{"exact saved", `{"pool":{"prefer_uids":["u1"],"prefer_mode":"only","prefer_exact":true}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Pool.PreferExact != tc.exact || cfg.Pool.PreferMode != "only" || !reflect.DeepEqual(cfg.Pool.PreferUIDs, []string{"u1"}) {
				t.Fatalf("incorrect preference loaded: uids=%v mode=%s exact=%v", cfg.Pool.PreferUIDs, cfg.Pool.PreferMode, cfg.Pool.PreferExact)
			}
			p := pool.New("")
			p.Add(&auth.Auth{UID: "u12"}) // Saved u1 no longer exists when the process starts.
			p.SetPrefer(cfg.Pool.PreferUIDs, cfg.Pool.PreferMode == "only", cfg.Pool.PreferExact)
			got := p.Pick("")
			if tc.exact && got != nil {
				t.Fatalf("reloaded exact UID expanded into %s", got.UID)
			}
			if !tc.exact && (got == nil || got.UID != "u12") {
				t.Fatalf("legacy fragment changed on reload: %v", got)
			}
		})
	}
}
