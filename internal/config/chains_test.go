package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"deposit-crediting/internal/domain"
)

func TestLoadChainsValid(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "chains.json")
	data := `[
		{
			"network_id": "stubchain",
			"chain_api_url": "http://localhost:8545",
			"poll_interval_ms": 12000
		},
		{
			"network_id": "fastchain",
			"chain_api_url": "http://localhost:8546",
			"poll_interval_ms": 2000
		}
	]`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	chains, err := LoadChains(path)
	if err != nil {
		t.Fatalf("LoadChains failed: %v", err)
	}
	if len(chains) != 2 {
		t.Fatalf("expected 2 chains, got %d", len(chains))
	}
	if chains[0].NetworkID != domain.NetworkStubchain {
		t.Errorf("expected stubchain, got %s", chains[0].NetworkID)
	}
	if chains[0].ChainAPIURL != "http://localhost:8545" {
		t.Errorf("expected http://localhost:8545, got %s", chains[0].ChainAPIURL)
	}
	if chains[0].PollInterval != 12*time.Second {
		t.Errorf("expected 12s, got %v", chains[0].PollInterval)
	}
	if chains[1].NetworkID != domain.NetworkFastchain {
		t.Errorf("expected fastchain, got %s", chains[1].NetworkID)
	}
	if chains[1].PollInterval != 2*time.Second {
		t.Errorf("expected 2s, got %v", chains[1].PollInterval)
	}
}

func TestLoadChainsRejectsUnknownNetwork(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "chains.json")
	data := `[{"network_id": "ethereum", "chain_api_url": "http://localhost:8545", "poll_interval_ms": 12000}]`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadChains(path); err == nil {
		t.Error("LoadChains accepted an unknown network_id; want error")
	}
}
