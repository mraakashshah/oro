package dispatcher //nolint:testpackage // white-box test exercises unexported config defaulting and validation

import (
	"strings"
	"testing"
)

func TestJanitorConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		wantErr   string
		assertCfg func(*testing.T, Config)
	}{
		{
			name: "audit requires janitor",
			cfg: Config{
				AuditEnabled:   true,
				JanitorEnabled: false,
			},
			wantErr: "AuditEnabled requires JanitorEnabled because audit counters are driven by janitor cycles",
		},
		{
			name: "enabled janitor requires runtime catalog",
			cfg: Config{
				JanitorEnabled: true,
			},
			wantErr: "JanitorEnabled requires StorageCatalogPath",
		},
		{
			name: "negative janitor interval",
			cfg: Config{
				JanitorInterval: -1,
			},
			wantErr: "JanitorInterval must be non-negative",
		},
		{
			name: "negative janitor idle threshold",
			cfg: Config{
				JanitorIdleThreshold: -1,
			},
			wantErr: "JanitorIdleThreshold must be non-negative",
		},
		{
			name: "negative audit cadence",
			cfg: Config{
				AuditEveryNJanitors: -1,
			},
			wantErr: "AuditEveryNJanitors must be non-negative",
		},
		{
			name: "negative janitor top k",
			cfg: Config{
				JanitorTopK: -1,
			},
			wantErr: "JanitorTopK must be non-negative",
		},
		{
			name: "zero values leave janitor disabled and idle threshold empty queue only",
			cfg:  Config{},
			assertCfg: func(t *testing.T, got Config) {
				t.Helper()
				if got.JanitorInterval != 0 {
					t.Fatalf("JanitorInterval: got %d, want 0 (disabled)", got.JanitorInterval)
				}
				if got.JanitorIdleThreshold != 0 {
					t.Fatalf("JanitorIdleThreshold: got %d, want 0 (empty queue only)", got.JanitorIdleThreshold)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved := tt.cfg.withDefaults()
			err := resolved.validate()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("validate() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate() error = %v", err)
			}
			tt.assertCfg(t, resolved)
		})
	}
}
