package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"deposit-crediting/internal/domain"
)

type ChainConfig struct {
	NetworkID      domain.NetworkID `json:"network_id"`
	ChainAPIURL    string           `json:"chain_api_url"`
	PollIntervalMs int64            `json:"poll_interval_ms"`
	PollInterval   time.Duration
}

func LoadChains(path string) ([]ChainConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var configs []ChainConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	for i := range configs {
		switch configs[i].NetworkID {
		case domain.NetworkStubchain, domain.NetworkFastchain:
		default:
			return nil, fmt.Errorf("config: unknown network_id %q", configs[i].NetworkID)
		}
		if configs[i].PollIntervalMs <= 0 {
			return nil, fmt.Errorf("config: chain %s poll_interval_ms must be positive", configs[i].NetworkID)
		}
		configs[i].PollInterval = time.Duration(configs[i].PollIntervalMs) * time.Millisecond
	}
	return configs, nil
}
