package config

import (
	"fmt"
	"math/big"

	"deposit-crediting/internal/domain"
)

// AssetConfigMap provides in-memory asset configuration lookup
type AssetConfigMap struct {
	configs map[domain.NetworkID]map[string]AssetConfig
}

// AssetConfig represents runtime asset configuration
type AssetConfig struct {
	Chain       domain.NetworkID
	Asset       string
	Decimals    int
	Mode        domain.Mode
	MinAmount   *big.Int
	NCredit     uint64
	NFinalize   uint64
	ReorgWindow uint64
	ExposureCap *big.Int // nil means no cap
	TierAmount  *big.Int // nil means all credits held during breach
}

// NewAssetConfigMap builds an in-memory config map from JSON entries
func NewAssetConfigMap(entries []AssetEntry) (*AssetConfigMap, error) {
	configs := make(map[domain.NetworkID]map[string]AssetConfig)

	for _, e := range entries {
		chainID := domain.NetworkID(e.Chain)
		if configs[chainID] == nil {
			configs[chainID] = make(map[string]AssetConfig)
		}

		minAmount, ok := new(big.Int).SetString(e.MinAmount, 10)
		if !ok {
			return nil, fmt.Errorf("config: invalid min_amount %q for %s/%s", e.MinAmount, e.Chain, e.Asset)
		}

		mode, err := parseMode(e.Mode)
		if err != nil {
			return nil, fmt.Errorf("config: %s/%s: %w", e.Chain, e.Asset, err)
		}

		cfg := AssetConfig{
			Chain:       chainID,
			Asset:       e.Asset,
			Decimals:    e.Decimals,
			Mode:        mode,
			MinAmount:   minAmount,
			NCredit:     uint64(e.NCredit),
			NFinalize:   uint64(e.NFinalize),
			ReorgWindow: uint64(e.ReorgWindow),
		}

		if e.ExposureCap != "" {
			cap, ok := new(big.Int).SetString(e.ExposureCap, 10)
			if !ok {
				return nil, fmt.Errorf("config: invalid exposure_cap %q for %s/%s", e.ExposureCap, e.Chain, e.Asset)
			}
			cfg.ExposureCap = cap
		}

		if e.TierAmount != "" {
			tier, ok := new(big.Int).SetString(e.TierAmount, 10)
			if !ok {
				return nil, fmt.Errorf("config: invalid tier_amount %q for %s/%s", e.TierAmount, e.Chain, e.Asset)
			}
			cfg.TierAmount = tier
		}

		configs[chainID][e.Asset] = cfg
	}

	return &AssetConfigMap{configs: configs}, nil
}

// Get returns the config for a specific (chain, asset) pair
func (m *AssetConfigMap) Get(chain domain.NetworkID, asset string) (AssetConfig, bool) {
	chainConfigs, ok := m.configs[chain]
	if !ok {
		return AssetConfig{}, false
	}
	cfg, ok := chainConfigs[asset]
	return cfg, ok
}

// ForChain returns all configs for a specific chain
func (m *AssetConfigMap) ForChain(chain domain.NetworkID) []AssetConfig {
	chainConfigs, ok := m.configs[chain]
	if !ok {
		return nil
	}
	result := make([]AssetConfig, 0, len(chainConfigs))
	for _, cfg := range chainConfigs {
		result = append(result, cfg)
	}
	return result
}

// ForChainAndMode returns configs for a specific chain and mode (self_built or custodian)
func (m *AssetConfigMap) ForChainAndMode(chain domain.NetworkID, mode domain.Mode) []AssetConfig {
	all := m.ForChain(chain)
	filtered := make([]AssetConfig, 0, len(all))
	for _, cfg := range all {
		if cfg.Mode == mode {
			filtered = append(filtered, cfg)
		}
	}
	return filtered
}

func parseMode(s string) (domain.Mode, error) {
	switch s {
	case "self_built":
		return domain.ModeSelfBuilt, nil
	case "custodian":
		return domain.ModeCustodian, nil
	default:
		return "", fmt.Errorf("invalid mode %q (expected self_built or custodian)", s)
	}
}
