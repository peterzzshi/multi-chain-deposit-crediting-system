// Package identity constructs the two-layer event identity used for
// deduplication (ADR 0001): source event IDs identify provider deliveries,
// logical transfer IDs identify creditable chain movements. All values are
// immutable and validated at construction; there is no I/O here.
package identity

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidPart categorizes ID construction violations: empty parts or
// parts containing the field separator.
var ErrInvalidPart = errors.New("identity: invalid ID part")

// SourceEventID identifies one provider delivery, e.g. one custodian
// webhook. It deduplicates retries and supports the audit trail; it never
// identifies the deposit itself.
type SourceEventID struct {
	Provider string
	EventID  string
}

// NewSourceEventID validates and constructs a SourceEventID.
func NewSourceEventID(provider, eventID string) (SourceEventID, error) {
	if !validPart(provider) || !validPart(eventID) {
		return SourceEventID{}, fmt.Errorf("%w: provider and event ID must be non-empty and contain no %q", ErrInvalidPart, ":")
	}
	return SourceEventID{Provider: provider, EventID: eventID}, nil
}

// String returns the canonical storage form "provider:eventID".
func (id SourceEventID) String() string { return id.Provider + ":" + id.EventID }

// LogicalTransferID identifies one creditable asset movement from stable
// chain facts. It deliberately excludes block hash, so a transaction
// re-included after a reorg keeps its identity. The sealed variants are
// TokenTransfer, NativeTransfer, and InternalNativeTransfer.
type LogicalTransferID interface {
	fmt.Stringer
	// ChainID returns the chain the transfer moved on.
	ChainID() string
	sealed()
}

// TokenTransfer identifies a token movement:
// (chain, transaction hash, contract, log index).
type TokenTransfer struct {
	Chain    string
	TxHash   string
	Contract string
	LogIndex int
}

// NewTokenTransfer validates and constructs a TokenTransfer.
func NewTokenTransfer(chain, txHash, contract string, logIndex int) (TokenTransfer, error) {
	if !validPart(chain) || !validPart(txHash) || !validPart(contract) {
		return TokenTransfer{}, fmt.Errorf("%w: chain, transaction hash and contract must be non-empty and contain no %q", ErrInvalidPart, ":")
	}
	if logIndex < 0 {
		return TokenTransfer{}, fmt.Errorf("%w: log index must be non-negative, got %d", ErrInvalidPart, logIndex)
	}
	return TokenTransfer{Chain: chain, TxHash: txHash, Contract: contract, LogIndex: logIndex}, nil
}

// String returns the canonical storage form.
func (t TokenTransfer) String() string {
	return fmt.Sprintf("%s:%s:token:%s:%d", t.Chain, t.TxHash, t.Contract, t.LogIndex)
}

// ChainID returns the chain the transfer moved on.
func (t TokenTransfer) ChainID() string { return t.Chain }

func (TokenTransfer) sealed() {}

// NativeTransfer identifies a direct native-coin movement. An externally
// signed transaction moves native value at most once, so
// (chain, transaction hash) is sufficient.
type NativeTransfer struct {
	Chain  string
	TxHash string
}

// NewNativeTransfer validates and constructs a NativeTransfer.
func NewNativeTransfer(chain, txHash string) (NativeTransfer, error) {
	if !validPart(chain) || !validPart(txHash) {
		return NativeTransfer{}, fmt.Errorf("%w: chain and transaction hash must be non-empty and contain no %q", ErrInvalidPart, ":")
	}
	return NativeTransfer{Chain: chain, TxHash: txHash}, nil
}

// String returns the canonical storage form.
func (t NativeTransfer) String() string { return t.Chain + ":" + t.TxHash + ":native" }

// ChainID returns the chain the transfer moved on.
func (t NativeTransfer) ChainID() string { return t.Chain }

func (NativeTransfer) sealed() {}

// InternalNativeTransfer identifies a native movement caused by contract
// execution: (chain, transaction hash, trace index).
type InternalNativeTransfer struct {
	Chain      string
	TxHash     string
	TraceIndex int
}

// NewInternalNativeTransfer validates and constructs an
// InternalNativeTransfer.
func NewInternalNativeTransfer(chain, txHash string, traceIndex int) (InternalNativeTransfer, error) {
	if !validPart(chain) || !validPart(txHash) {
		return InternalNativeTransfer{}, fmt.Errorf("%w: chain and transaction hash must be non-empty and contain no %q", ErrInvalidPart, ":")
	}
	if traceIndex < 0 {
		return InternalNativeTransfer{}, fmt.Errorf("%w: trace index must be non-negative, got %d", ErrInvalidPart, traceIndex)
	}
	return InternalNativeTransfer{Chain: chain, TxHash: txHash, TraceIndex: traceIndex}, nil
}

// String returns the canonical storage form.
func (t InternalNativeTransfer) String() string {
	return fmt.Sprintf("%s:%s:trace:%d", t.Chain, t.TxHash, t.TraceIndex)
}

// ChainID returns the chain the transfer moved on.
func (t InternalNativeTransfer) ChainID() string { return t.Chain }

func (InternalNativeTransfer) sealed() {}

// validPart reports whether s can appear in a canonical ID: non-empty and
// free of the field separator.
func validPart(s string) bool { return s != "" && !strings.Contains(s, ":") }
