package adapters

import (
	"context"
	"fmt"
	"math/big"

	"deposit-crediting/internal/domain"
)

type TransferKind string

const (
	Native   TransferKind = "native"
	Token    TransferKind = "token"
	Internal TransferKind = "internal"
)

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

func (t Transfer) LogicalID(networkID domain.NetworkID) (domain.LogicalTransferID, error) {
	switch t.Kind {
	case Token:
		return domain.NewTokenTransfer(networkID, t.TxHash, t.Asset, t.LogIndex)
	case Native:
		return domain.NewNativeTransfer(networkID, t.TxHash)
	case Internal:
		return domain.NewInternalNativeTransfer(networkID, t.TxHash, t.TraceIndex)
	}
	return nil, fmt.Errorf("chain: unknown transfer kind %q", t.Kind)
}

type Block struct {
	Height     uint64
	Hash       string
	ParentHash string
	Transfers  []Transfer
}

type TxLocation struct {
	Height    uint64
	Hash      string
	Transfers []Transfer
}

type Client interface {
	Head(ctx context.Context) (uint64, error)
	BlockHash(ctx context.Context, height uint64) (hash string, found bool, err error)
	Block(ctx context.Context, height uint64) (Block, error)
	TxByHash(ctx context.Context, txHash string) (TxLocation, bool, error)
}
