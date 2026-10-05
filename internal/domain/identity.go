package domain

import (
	"fmt"
	"strings"

	"deposit-crediting/internal/errs"
)

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

type LogicalTransferID interface {
	fmt.Stringer
	NetworkID() NetworkID
	sealed()
}

type TokenTransfer struct {
	Chain    NetworkID
	TxHash   string
	Contract string
	LogIndex int
}

func NewTokenTransfer(c NetworkID, txHash, contract string, logIndex int) (TokenTransfer, error) {
	if !validParts(string(c), txHash, contract) {
		return TokenTransfer{}, fmt.Errorf("%w: chain, txHash, contract", errs.ErrInvalidPart)
	}
	if logIndex < 0 {
		return TokenTransfer{}, fmt.Errorf("%w: logIndex %d", errs.ErrInvalidPart, logIndex)
	}
	return TokenTransfer{Chain: c, TxHash: txHash, Contract: contract, LogIndex: logIndex}, nil
}

func (t TokenTransfer) String() string {
	return fmt.Sprintf("%s:%s:token:%s:%d", t.Chain, t.TxHash, t.Contract, t.LogIndex)
}

func (t TokenTransfer) NetworkID() NetworkID { return t.Chain }

func (TokenTransfer) sealed() {}

type NativeTransfer struct {
	Chain  NetworkID
	TxHash string
}

func NewNativeTransfer(c NetworkID, txHash string) (NativeTransfer, error) {
	if !validParts(string(c), txHash) {
		return NativeTransfer{}, fmt.Errorf("%w: chain, txHash", errs.ErrInvalidPart)
	}
	return NativeTransfer{Chain: c, TxHash: txHash}, nil
}

func (t NativeTransfer) String() string { return string(t.Chain) + ":" + t.TxHash + ":native" }

func (t NativeTransfer) NetworkID() NetworkID { return t.Chain }

func (NativeTransfer) sealed() {}

type InternalNativeTransfer struct {
	Chain      NetworkID
	TxHash     string
	TraceIndex int
}

func NewInternalNativeTransfer(c NetworkID, txHash string, traceIndex int) (InternalNativeTransfer, error) {
	if !validParts(string(c), txHash) {
		return InternalNativeTransfer{}, fmt.Errorf("%w: chain, txHash", errs.ErrInvalidPart)
	}
	if traceIndex < 0 {
		return InternalNativeTransfer{}, fmt.Errorf("%w: traceIndex %d", errs.ErrInvalidPart, traceIndex)
	}
	return InternalNativeTransfer{Chain: c, TxHash: txHash, TraceIndex: traceIndex}, nil
}

func (t InternalNativeTransfer) String() string {
	return fmt.Sprintf("%s:%s:trace:%d", t.Chain, t.TxHash, t.TraceIndex)
}

func (t InternalNativeTransfer) NetworkID() NetworkID { return t.Chain }

func (InternalNativeTransfer) sealed() {}

func validParts(parts ...string) bool {
	for _, p := range parts {
		if p == "" || strings.Contains(p, ":") {
			return false
		}
	}
	return true
}
