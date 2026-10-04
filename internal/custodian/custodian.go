// Package custodian implements the custodian-mode ingest path (ADR 0004):
// claims arrive via webhooks (duplicated, out-of-order, delayed) and the
// reconciliation poll, but a deposit is credited only after the claim is
// verified against the chain — chain facts are the source of truth. The
// re-checker then drives depth transitions and reorg handling for
// custodian deposits, mirroring what the scanner does in self-built mode.
package custodian

import (
	"context"
	"math/big"
	"time"

	"deposit-crediting/internal/adapters/chain"
	"deposit-crediting/internal/domain/deposit"
)

type Config struct {
	ChainID      string
	Provider     string // source-events namespace, e.g. "custodianA"
	PollInterval time.Duration
}

// Claim is one deposit as the custodian reports it, via webhook or query
// API. Untrusted until verified on-chain. Kind/LogIndex/TraceIndex are
// the provider's transfer discriminator; without them a claim is
// ambiguous when one transaction carries several equal transfers.
type Claim struct {
	ProviderEventID string
	Chain           string
	TxHash          string
	To              string
	Asset           string
	Amount          *big.Int
	Kind            chain.TransferKind
	LogIndex        *int
	TraceIndex      *int
	ObservedAt      time.Time
}

// Provider is the custodian query API; the reconciliation poller uses it
// to recover claims the webhook channel duplicated, delayed, or dropped.
type Provider interface {
	// FetchDeposits returns deposits observed at or after since.
	FetchDeposits(ctx context.Context, since time.Time) ([]Claim, error)
	// FetchVaultTotal returns the custodian's total vault holdings for a
	// (chain, asset) — the solvency reference for our ledger total.
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
	State       deposit.State
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
	State      deposit.State
	Height     uint64
	BlockHash  string
}

// Reorged is a deposit whose inclusion left the canonical chain: REORGED
// (window open) or REVERSED (still watched for re-inclusion, ADR 0005).
type Reorged struct {
	TransferID string
	Asset      string
	State      deposit.State
	TxHash     string
	Height     *uint64
}

// Store is the custodian path's persistence boundary; implemented by
// internal/store. All queries are scoped to one chain and to custodian
// mode.
type Store interface {
	SourceEventSeen(ctx context.Context, provider, eventID string) (bool, error)
	RecordSourceEvent(ctx context.Context, provider string, cl Claim) error
	// ResolveAddress maps a custodian deposit address to its account.
	ResolveAddress(ctx context.Context, chainID, address string) (string, bool, error)
	AssetConfig(ctx context.Context, chainID, asset string) (AssetConfig, bool, error)
	AssetConfigs(ctx context.Context, chainID string) ([]AssetConfig, error)
	// OpenDeposit inserts a new deposit row; false means the transfer ID
	// already exists.
	OpenDeposit(ctx context.Context, p OpenParams) (bool, error)
	DepositState(ctx context.Context, transferID string) (deposit.State, error)
	TrackedDeposits(ctx context.Context, chainID string) ([]Tracked, error)
	ReorgedDeposits(ctx context.Context, chainID string) ([]Reorged, error)
	MarkReorged(ctx context.Context, transferID string, height uint64) error
	ReincludeDeposit(ctx context.Context, transferID string, height uint64, hash string) error
	HasCredit(ctx context.Context, transferID string) (bool, error)
	// LedgerTotal sums the balances of all accounts holding custodian
	// deposits of (chain, asset) — the solvency check's ledger side.
	LedgerTotal(ctx context.Context, chainID, asset string) (*big.Int, error)
}
