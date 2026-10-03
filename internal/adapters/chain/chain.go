// Package chain defines the read model of a chain node shared by the
// scanner and the custodian ingest path. Chain specifics (RPC, receipt vs
// trace extraction) live behind Client in per-chain adapters.
package chain

import (
	"context"
	"fmt"
	"math/big"

	"deposit-crediting/internal/domain/identity"
)

// TransferKind distinguishes how a value movement is observed on-chain.
type TransferKind string

const (
	Native   TransferKind = "native"
	Token    TransferKind = "token"
	Internal TransferKind = "internal"
)

// Transfer is one value movement extracted from a block. Asset is the
// native symbol or the token contract; LogIndex applies to Token,
// TraceIndex to Internal.
type Transfer struct {
	Kind       TransferKind
	TxHash     string
	From       string
	To         string
	Asset      string
	Amount     *big.Int
	LogIndex   int
	TraceIndex int
}

// LogicalID derives the crediting identity of the transfer (ADR 0001
// second layer).
func (t Transfer) LogicalID(chainID string) (identity.LogicalTransferID, error) {
	switch t.Kind {
	case Token:
		return identity.NewTokenTransfer(chainID, t.TxHash, t.Asset, t.LogIndex)
	case Native:
		return identity.NewNativeTransfer(chainID, t.TxHash)
	case Internal:
		return identity.NewInternalNativeTransfer(chainID, t.TxHash, t.TraceIndex)
	}
	return nil, fmt.Errorf("chain: unknown transfer kind %q", t.Kind)
}

// Block is one canonical block.
type Block struct {
	Height     uint64
	Hash       string
	ParentHash string
	Transfers  []Transfer
}

// TxLocation is where a transaction sits on the canonical chain, plus the
// transfers it carried.
type TxLocation struct {
	Height    uint64
	Hash      string
	Transfers []Transfer
}

// Client is the read model of a chain node.
type Client interface {
	Head(ctx context.Context) (uint64, error)
	BlockHash(ctx context.Context, height uint64) (string, error)
	Block(ctx context.Context, height uint64) (Block, error)
	// TxByHash locates a transaction on the canonical chain; false means
	// it is not (currently) canonical — never mined or reorged out.
	TxByHash(ctx context.Context, txHash string) (TxLocation, bool, error)
}
