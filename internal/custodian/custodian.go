package custodian

import (
	"context"
	"math/big"
	"time"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/domain"
)

type Config struct {
	ChainID      domain.NetworkID
	Provider     string
	PollInterval time.Duration
}

type Claim struct {
	ProviderEventID string
	Chain           domain.NetworkID
	TxHash          string
	To              string
	Asset           string
	Amount          *big.Int
	Kind            adapters.TransferKind
	LogIndex        *int
	TraceIndex      *int
	ObservedAt      time.Time
}

type Provider interface {
	FetchDeposits(ctx context.Context, since time.Time) ([]Claim, error)
	FetchVaultTotal(ctx context.Context, chain, asset string) (*big.Int, error)
}

type AssetConfig struct {
	Asset       string
	MinAmount   *big.Int
	NCredit     uint64
	NFinalize   uint64
	ReorgWindow uint64
}

type OpenParams struct {
	TransferID  string
	State       domain.State
	Chain       string
	Asset       string
	Account     string
	Address     string
	Amount      *big.Int
	Height      uint64
	Hash        string
	TxHash      string
	SourceEvent string
}

type Tracked struct {
	TransferID string
	Asset      string
	State      domain.State
	Height     uint64
	BlockHash  string
}

type Reorged struct {
	TransferID string
	Asset      string
	State      domain.State
	TxHash     string
	Height     *uint64
}

type Store interface {
	SourceEventSeen(ctx context.Context, provider, eventID string) (bool, error)
	RecordSourceEvent(ctx context.Context, provider string, cl Claim) error
	ResolveAddress(ctx context.Context, chainID, address string) (string, bool, error)
	AssetConfig(ctx context.Context, chainID, asset string) (AssetConfig, bool, error)
	AssetConfigs(ctx context.Context, chainID string) ([]AssetConfig, error)
	OpenDeposit(ctx context.Context, p OpenParams) (domain.OpenResult, error)
	DepositState(ctx context.Context, transferID string) (domain.State, error)
	TrackedDeposits(ctx context.Context, chainID string) ([]Tracked, error)
	ReorgedDeposits(ctx context.Context, chainID string) ([]Reorged, error)
	MarkReorged(ctx context.Context, transferID string, height uint64) error
	ReincludeDeposit(ctx context.Context, transferID string, height uint64, hash string) error
	HasCredit(ctx context.Context, transferID string) (bool, error)
	LedgerTotal(ctx context.Context, chainID, asset string) (*big.Int, error)
}
