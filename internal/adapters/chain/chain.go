// Package chain defines the scanner's read model of a chain node. Chain
// specifics (RPC, receipt vs trace extraction) live behind Client in
// per-chain adapters.
package chain

import (
	"context"
	"math/big"
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

// Block is one canonical block.
type Block struct {
	Height     uint64
	Hash       string
	ParentHash string
	Transfers  []Transfer
}

// Client is the scanner's read model of a chain node.
type Client interface {
	Head(ctx context.Context) (uint64, error)
	BlockHash(ctx context.Context, height uint64) (string, error)
	Block(ctx context.Context, height uint64) (Block, error)
}
