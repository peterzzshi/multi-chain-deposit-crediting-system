package risk

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"deposit-crediting/internal/domain"
)

type Config struct {
	NetworkID    domain.NetworkID
	PollInterval time.Duration
}

type AssetRisk struct {
	Asset string
	Cap   *big.Int
}

type Held struct {
	TransferID string
	Account    string
	Asset      string
	Amount     *big.Int
}

type Mismatch struct {
	Account string
	Asset   string
	Ledger  *big.Int
	Balance *big.Int
}

type Reorged struct {
	TransferID string
	Asset      string
	Height     *uint64
}

type Store interface {
	RiskConfigs(ctx context.Context, networkID string) ([]AssetRisk, error)
	Exposure(ctx context.Context, networkID, asset string) (*big.Int, error)
	ExposureState(ctx context.Context, networkID, asset string) (holdsActive bool, err error)
	WriteExposure(ctx context.Context, networkID, asset string, exposure *big.Int, holdsActive bool) error
	HeldDeposits(ctx context.Context, networkID, asset string) ([]Held, error)
	ReleaseHold(ctx context.Context, h Held) error
	CreditsMissingEntry(ctx context.Context, networkID string) ([]string, error)
	LedgerBalanceMismatches(ctx context.Context) ([]Mismatch, error)
	ReorgedDeposits(ctx context.Context, networkID string) ([]Reorged, error)
	Windows(ctx context.Context, networkID string) (map[string]uint64, error)
}

type Alert struct {
	Level   string
	Chain   string
	Asset   string
	Message string
}

type Monitor struct {
	cfg   Config
	store Store
}

const (
	warnPercent    = 80
	releasePercent = 70
)

func NewMonitor(cfg Config, st Store) *Monitor {
	return &Monitor{cfg: cfg, store: st}
}

func (m *Monitor) Run(ctx context.Context) error {
	if m.cfg.PollInterval <= 0 {
		return fmt.Errorf("risk monitor: poll interval must be positive")
	}
	ticker := time.NewTicker(m.cfg.PollInterval)
	defer ticker.Stop()
	for {
		alerts, err := m.Tick(ctx)
		if err != nil {
			slog.Error("risk monitor tick failed", "chain", m.cfg.NetworkID, "err", err)
		}
		for _, a := range alerts {
			slog.Log(ctx, alertLevel(a.Level), "exposure alert", "chain", a.Chain, "asset", a.Asset, "msg", a.Message)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Monitor) Tick(ctx context.Context) ([]Alert, error) {
	configs, err := m.store.RiskConfigs(ctx, string(m.cfg.NetworkID))
	if err != nil {
		return nil, err
	}
	var alerts []Alert
	for _, c := range configs {
		if c.Cap == nil || c.Cap.Sign() <= 0 {
			continue
		}
		exposure, err := m.store.Exposure(ctx, string(m.cfg.NetworkID), c.Asset)
		if err != nil {
			return nil, err
		}
		active, err := m.store.ExposureState(ctx, string(m.cfg.NetworkID), c.Asset)
		if err != nil {
			return nil, err
		}
		pct := new(big.Int).Mul(exposure, big.NewInt(100))
		pct.Div(pct, c.Cap)
		switch {
		case pct.Cmp(big.NewInt(100)) >= 0:
			active = true
			alerts = append(alerts, Alert{Level: "critical", Chain: string(m.cfg.NetworkID), Asset: c.Asset,
				Message: "exposure " + exposure.String() + " >= cap " + c.Cap.String() + "; spendability holds active"})
		case !active && pct.Cmp(big.NewInt(warnPercent)) >= 0:
			alerts = append(alerts, Alert{Level: "warn", Chain: string(m.cfg.NetworkID), Asset: c.Asset,
				Message: "exposure " + exposure.String() + " at " + pct.String() + "% of cap " + c.Cap.String()})
		case active && pct.Cmp(big.NewInt(releasePercent)) <= 0:
			active = false
			if err := m.releaseHolds(ctx, c.Asset); err != nil {
				return nil, err
			}
		}
		if err := m.store.WriteExposure(ctx, string(m.cfg.NetworkID), c.Asset, exposure, active); err != nil {
			return nil, err
		}
	}
	return alerts, nil
}

func (m *Monitor) releaseHolds(ctx context.Context, asset string) error {
	held, err := m.store.HeldDeposits(ctx, string(m.cfg.NetworkID), asset)
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
