// Package config loads the supported-assets file (configs/assets.json):
// which (chain, asset) pairs the system credits, in which mode, and with
// which risk policy. Binaries apply it at startup; invalid files fail
// fast instead of running with a silent misconfiguration.
package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
)

type AssetEntry struct {
	Chain       string `json:"chain"`
	Asset       string `json:"asset"`
	Decimals    int    `json:"decimals"`
	Mode        string `json:"mode"`
	MinAmount   string `json:"min_amount"`
	NCredit     int    `json:"n_credit"`
	NFinalize   int    `json:"n_finalize"`
	ReorgWindow int    `json:"reorg_window"`
	ExposureCap string `json:"exposure_cap,omitempty"`
	TierAmount  string `json:"tier_amount,omitempty"`
}

func LoadAssets(path string) ([]AssetEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var entries []AssetEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if err := e.validate(); err != nil {
			return nil, err
		}
		key := e.Chain + "/" + e.Asset
		if seen[key] {
			return nil, fmt.Errorf("config: duplicate entry %s", key)
		}
		seen[key] = true
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("config: %s lists no assets", path)
	}
	return entries, nil
}

func (e AssetEntry) validate() error {
	if e.Chain == "" || e.Asset == "" {
		return fmt.Errorf("config: chain and asset are required: %+v", e)
	}
	switch e.Mode {
	case "self_built", "custodian":
	default:
		return fmt.Errorf("config: %s/%s: mode must be self_built or custodian, got %q", e.Chain, e.Asset, e.Mode)
	}
	if e.Decimals < 0 {
		return fmt.Errorf("config: %s/%s: decimals must be >= 0", e.Chain, e.Asset)
	}
	if !positive(e.MinAmount) {
		return fmt.Errorf("config: %s/%s: min_amount must be a positive integer, got %q", e.Chain, e.Asset, e.MinAmount)
	}
	if e.NCredit < 1 || e.NFinalize < e.NCredit {
		return fmt.Errorf("config: %s/%s: need 1 <= n_credit <= n_finalize, got %d/%d", e.Chain, e.Asset, e.NCredit, e.NFinalize)
	}
	if e.ReorgWindow < 1 {
		return fmt.Errorf("config: %s/%s: reorg_window must be positive", e.Chain, e.Asset)
	}
	if e.ExposureCap != "" && !positive(e.ExposureCap) {
		return fmt.Errorf("config: %s/%s: exposure_cap must be a positive integer, got %q", e.Chain, e.Asset, e.ExposureCap)
	}
	if e.TierAmount != "" && !positive(e.TierAmount) {
		return fmt.Errorf("config: %s/%s: tier_amount must be a positive integer, got %q", e.Chain, e.Asset, e.TierAmount)
	}
	return nil
}

func positive(s string) bool {
	n, ok := new(big.Int).SetString(s, 10)
	return ok && n.Sign() > 0
}
