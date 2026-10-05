package config

import (
	"encoding/json"
	"fmt"
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
	return entries, nil
}
