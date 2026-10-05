package adapters

import (
	"fmt"
	"math/big"
)

type TransferJSON struct {
	Kind       TransferKind `json:"kind"`
	TxHash     string       `json:"txHash"`
	From       string       `json:"from,omitempty"`
	To         string       `json:"to"`
	Asset      string       `json:"asset"`
	Amount     string       `json:"amount"`
	LogIndex   int          `json:"logIndex,omitempty"`
	TraceIndex int          `json:"traceIndex,omitempty"`
}

func (t TransferJSON) ToTransfer() (Transfer, error) {
	amount, ok := new(big.Int).SetString(t.Amount, 10)
	if !ok {
		return Transfer{}, fmt.Errorf("amount must be a base-10 integer string")
	}
	return Transfer{
		Kind:       t.Kind,
		TxHash:     t.TxHash,
		From:       t.From,
		To:         t.To,
		Asset:      t.Asset,
		Amount:     amount,
		LogIndex:   t.LogIndex,
		TraceIndex: t.TraceIndex,
	}, nil
}

func FromTransfer(t Transfer) TransferJSON {
	amount := "0"
	if t.Amount != nil {
		amount = t.Amount.String()
	}
	return TransferJSON{
		Kind:       t.Kind,
		TxHash:     t.TxHash,
		From:       t.From,
		To:         t.To,
		Asset:      t.Asset,
		Amount:     amount,
		LogIndex:   t.LogIndex,
		TraceIndex: t.TraceIndex,
	}
}
