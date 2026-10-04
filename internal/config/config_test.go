package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "assets.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestLoadAssetsValid(t *testing.T) {
	path := writeTemp(t, `[
		{"chain":"evm","asset":"ETH","decimals":18,"mode":"self_built","min_amount":"100","n_credit":2,"n_finalize":4,"reorg_window":3,"exposure_cap":"5000"},
		{"chain":"evm","asset":"USDT","decimals":6,"mode":"custodian","min_amount":"50","n_credit":2,"n_finalize":4,"reorg_window":3}
	]`)
	entries, err := LoadAssets(path)
	if err != nil {
		t.Fatalf("LoadAssets() unexpected error: %v", err)
	}
	if got, want := len(entries), 2; got != want {
		t.Fatalf("entries = %d; want %d", got, want)
	}
	if entries[0].ExposureCap != "5000" || entries[1].ExposureCap != "" {
		t.Errorf("exposure caps = %q, %q; want 5000, \"\"", entries[0].ExposureCap, entries[1].ExposureCap)
	}
}

func TestLoadAssetsRejectsInvalid(t *testing.T) {
	base := `{"chain":"evm","asset":"ETH","decimals":18,"mode":"self_built","min_amount":"100","n_credit":2,"n_finalize":4,"reorg_window":3}`
	tests := []struct {
		name    string
		content string
	}{
		{"empty list", `[]`},
		{"bad json", `{`},
		{"bad mode", `[{"chain":"evm","asset":"ETH","decimals":18,"mode":"hybrid","min_amount":"100","n_credit":2,"n_finalize":4,"reorg_window":3}]`},
		{"zero min amount", `[{"chain":"evm","asset":"ETH","decimals":18,"mode":"self_built","min_amount":"0","n_credit":2,"n_finalize":4,"reorg_window":3}]`},
		{"n_finalize below n_credit", `[{"chain":"evm","asset":"ETH","decimals":18,"mode":"self_built","min_amount":"100","n_credit":4,"n_finalize":2,"reorg_window":3}]`},
		{"zero reorg window", `[{"chain":"evm","asset":"ETH","decimals":18,"mode":"self_built","min_amount":"100","n_credit":2,"n_finalize":4,"reorg_window":0}]`},
		{"bad exposure cap", `[{"chain":"evm","asset":"ETH","decimals":18,"mode":"self_built","min_amount":"100","n_credit":2,"n_finalize":4,"reorg_window":3,"exposure_cap":"-5"}]`},
		{"duplicate pair", `[` + base + `,` + base + `]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadAssets(writeTemp(t, tt.content)); err == nil {
				t.Error("LoadAssets() = nil error; want validation failure")
			}
		})
	}
}
