// Package identity constructs the two-layer event identity (ADR 0001):
// source event IDs identify provider deliveries, logical transfer IDs
// identify creditable chain movements.
package identity

import (
	"fmt"
	"strings"

	"deposit-crediting/internal/errs"
)

// SourceEventID identifies one provider delivery, e.g. one custodian
// webhook. It never identifies the deposit itself.
type SourceEventID struct {
	Provider string
	EventID  string
}

func NewSourceEventID(provider, eventID string) (SourceEventID, error) {
	if !validParts(provider, eventID) {
		return SourceEventID{}, fmt.Errorf("%w: provider, eventID", errs.ErrInvalidPart)
	}
	return SourceEventID{Provider: provider, EventID: eventID}, nil
}

func (id SourceEventID) String() string { return id.Provider + ":" + id.EventID }

// LogicalTransferID identifies one creditable asset movement from stable
// chain facts — it excludes block hash, so a re-included transfer keeps its
// identity. sealed() keeps the variant set closed to this package, so
// switches on the three variants stay exhaustive.
type LogicalTransferID interface {
	fmt.Stringer
	ChainID() string
	sealed()
}

// TokenTransfer is (chain, transaction hash, contract, log index).
type TokenTransfer struct {
	Chain    string
	TxHash   string
	Contract string
	LogIndex int
}

func NewTokenTransfer(chain, txHash, contract string, logIndex int) (TokenTransfer, error) {
	if !validParts(chain, txHash, contract) {
		return TokenTransfer{}, fmt.Errorf("%w: chain, txHash, contract", errs.ErrInvalidPart)
	}
	if logIndex < 0 {
		return TokenTransfer{}, fmt.Errorf("%w: logIndex %d", errs.ErrInvalidPart, logIndex)
	}
	return TokenTransfer{Chain: chain, TxHash: txHash, Contract: contract, LogIndex: logIndex}, nil
}

func (t TokenTransfer) String() string {
	return fmt.Sprintf("%s:%s:token:%s:%d", t.Chain, t.TxHash, t.Contract, t.LogIndex)
}

func (t TokenTransfer) ChainID() string { return t.Chain }

func (TokenTransfer) sealed() {}

// NativeTransfer is (chain, transaction hash): an externally signed
// transaction moves native value at most once.
type NativeTransfer struct {
	Chain  string
	TxHash string
}

func NewNativeTransfer(chain, txHash string) (NativeTransfer, error) {
	if !validParts(chain, txHash) {
		return NativeTransfer{}, fmt.Errorf("%w: chain, txHash", errs.ErrInvalidPart)
	}
	return NativeTransfer{Chain: chain, TxHash: txHash}, nil
}

func (t NativeTransfer) String() string { return t.Chain + ":" + t.TxHash + ":native" }

func (t NativeTransfer) ChainID() string { return t.Chain }

func (NativeTransfer) sealed() {}

// InternalNativeTransfer is (chain, transaction hash, trace index): a
// native movement caused by contract execution.
type InternalNativeTransfer struct {
	Chain      string
	TxHash     string
	TraceIndex int
}

func NewInternalNativeTransfer(chain, txHash string, traceIndex int) (InternalNativeTransfer, error) {
	if !validParts(chain, txHash) {
		return InternalNativeTransfer{}, fmt.Errorf("%w: chain, txHash", errs.ErrInvalidPart)
	}
	if traceIndex < 0 {
		return InternalNativeTransfer{}, fmt.Errorf("%w: traceIndex %d", errs.ErrInvalidPart, traceIndex)
	}
	return InternalNativeTransfer{Chain: chain, TxHash: txHash, TraceIndex: traceIndex}, nil
}

func (t InternalNativeTransfer) String() string {
	return fmt.Sprintf("%s:%s:trace:%d", t.Chain, t.TxHash, t.TraceIndex)
}

func (t InternalNativeTransfer) ChainID() string { return t.Chain }

func (InternalNativeTransfer) sealed() {}

func validParts(parts ...string) bool {
	for _, p := range parts {
		if p == "" || strings.Contains(p, ":") {
			return false
		}
	}
	return true
}
