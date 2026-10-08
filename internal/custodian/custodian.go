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

type Store interface {
	SourceEventSeen(ctx context.Context, provider, eventID string) (bool, error)
	RecordSourceEvent(ctx context.Context, provider string, cl Claim) error
	ResolveAddress(ctx context.Context, chainID, address string) (string, bool, error)
	// OpenDeposit returns StateCreated if created, or the existing state if already exists.
	OpenDeposit(ctx context.Context, p domain.OpenDepositParams) (domain.State, error)
	TrackedDeposits(ctx context.Context, chainID string) ([]domain.TrackedDeposit, error)
	ReorgedDeposits(ctx context.Context, chainID string) ([]domain.ReorgedDeposit, error)
	MarkReorged(ctx context.Context, transferID string, height uint64) error
	ReincludeDeposit(ctx context.Context, transferID string, height uint64, hash string) error
	HasCredit(ctx context.Context, transferID string) (bool, error)
	LedgerTotal(ctx context.Context, chainID, asset string) (*big.Int, error)
}
