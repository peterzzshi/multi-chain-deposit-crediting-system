// Package risk implements the runtime risk controls of risk-policy §4:
// the exposure monitor (unfinalized spendable value vs E_max, with
// spendability holds on breach) and the runtime invariant monitors
// (mirroring the test invariants, Q17).
package risk

import (
	"context"
	"log/slog"
	"math/big"
	"time"
)

type Config struct {
	ChainID      string
	PollInterval time.Duration
}

// AssetRisk is the enforcement config for one asset; nil Cap means the
// asset is not exposure-capped.
type AssetRisk struct {
	Asset string
	Cap   *big.Int
}

// Held is a credited deposit currently spendability-held.
type Held struct {
	TransferID string
	Account    string
	Asset      string
	Amount     *big.Int
}

// Mismatch is a ledger-replay-vs-balance disagreement.
type Mismatch struct {
	Account string
	Asset   string
	Ledger  *big.Int
	Balance *big.Int
}

// Reorged is a deposit in REORGED state with its window-start height.
type Reorged struct {
	TransferID string
	Asset      string
	Height     *uint64
}

// Store is the risk package's persistence boundary; implemented by
// internal/store.
type Store interface {
	RiskConfigs(ctx context.Context, chainID string) ([]AssetRisk, error)
	// Exposure sums amounts of CREDITED, non-held deposits (unfinalized,
	// spendable — the value at reorg risk).
	Exposure(ctx context.Context, chainID, asset string) (*big.Int, error)
	ExposureState(ctx context.Context, chainID, asset string) (holdsActive bool, err error)
	WriteExposure(ctx context.Context, chainID, asset string, exposure *big.Int, holdsActive bool) error
	HeldDeposits(ctx context.Context, chainID, asset string) ([]Held, error)
	ReleaseHold(ctx context.Context, h Held) error
	// Invariant queries.
	CreditsMissingEntry(ctx context.Context, chainID string) ([]string, error)
	LedgerBalanceMismatches(ctx context.Context) ([]Mismatch, error)
	ReorgedDeposits(ctx context.Context, chainID string) ([]Reorged, error)
	// Windows maps asset to its reorg window (max across modes).
	Windows(ctx context.Context, chainID string) (map[string]uint64, error)
}

// Alert is one monitor finding; Level is "warn" or "critical".
type Alert struct {
	Level   string
	Chain   string
	Asset   string
	Message string
}

// Monitor tracks unfinalized spendable exposure per asset against its
// cap. Breach activates spendability holds on new credits (credits still
// post — the ledger must mirror chain facts); draining below the release
// threshold deactivates holds and releases held deposits.
type Monitor struct {
	cfg   Config
	store Store
}

// Breach at >= 100% of cap, warn at >= 80%, release holds at <= 70% —
// the hysteresis band prevents flapping around the cap.
const (
	warnPercent    = 80
	releasePercent = 70
)

func NewMonitor(cfg Config, st Store) *Monitor {
	return &Monitor{cfg: cfg, store: st}
}

// Run monitors until ctx is done; tick errors are logged and retried.
func (m *Monitor) Run(ctx context.Context) error {
	for {
		alerts, err := m.Tick(ctx)
		if err != nil {
			slog.Error("risk monitor tick failed", "chain", m.cfg.ChainID, "err", err)
		}
		for _, a := range alerts {
			slog.Log(ctx, alertLevel(a.Level), "exposure alert", "chain", a.Chain, "asset", a.Asset, "msg", a.Message)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.cfg.PollInterval):
		}
	}
}

// Tick recomputes exposure for every capped asset on the chain, toggles
// holds, and releases held deposits once exposure drains.
func (m *Monitor) Tick(ctx context.Context) ([]Alert, error) {
	configs, err := m.store.RiskConfigs(ctx, m.cfg.ChainID)
	if err != nil {
		return nil, err
	}
	var alerts []Alert
	for _, c := range configs {
		if c.Cap == nil || c.Cap.Sign() <= 0 {
			continue
		}
		exposure, err := m.store.Exposure(ctx, m.cfg.ChainID, c.Asset)
		if err != nil {
			return nil, err
		}
		active, err := m.store.ExposureState(ctx, m.cfg.ChainID, c.Asset)
		if err != nil {
			return nil, err
		}
		pct := new(big.Int).Mul(exposure, big.NewInt(100))
		pct.Div(pct, c.Cap)
		switch {
		case pct.Cmp(big.NewInt(100)) >= 0:
			active = true
			alerts = append(alerts, Alert{Level: "critical", Chain: m.cfg.ChainID, Asset: c.Asset,
				Message: "exposure " + exposure.String() + " >= cap " + c.Cap.String() + "; spendability holds active"})
		case !active && pct.Cmp(big.NewInt(warnPercent)) >= 0:
			alerts = append(alerts, Alert{Level: "warn", Chain: m.cfg.ChainID, Asset: c.Asset,
				Message: "exposure " + exposure.String() + " at " + pct.String() + "% of cap " + c.Cap.String()})
		case active && pct.Cmp(big.NewInt(releasePercent)) <= 0:
			active = false
			if err := m.releaseHolds(ctx, c.Asset); err != nil {
				return nil, err
			}
		}
		if err := m.store.WriteExposure(ctx, m.cfg.ChainID, c.Asset, exposure, active); err != nil {
			return nil, err
		}
	}
	return alerts, nil
}

func (m *Monitor) releaseHolds(ctx context.Context, asset string) error {
	held, err := m.store.HeldDeposits(ctx, m.cfg.ChainID, asset)
	if err != nil {
		return err
	}
	for _, h := range held {
		if err := m.store.ReleaseHold(ctx, h); err != nil {
			return err
		}
	}
	return nil
}

func alertLevel(level string) slog.Level {
	if level == "critical" {
		return slog.LevelError
	}
	return slog.LevelWarn
}
