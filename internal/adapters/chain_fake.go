package adapters

import (
	"context"
	"fmt"
	"sync"
)

type Chain struct {
	mu           sync.Mutex
	branch       int
	blocks       []Block
	BeforeBlock  func(height uint64)
	BlockHashErr error
}

func NewChain() *Chain {
	return &Chain{}
}

func (c *Chain) CorruptParent(height uint64, parent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocks[height-1].ParentHash = parent
}

func (c *Chain) AddBlock(transfers ...Transfer) Block {
	c.mu.Lock()
	defer c.mu.Unlock()
	height := uint64(len(c.blocks)) + 1
	parent := "0x0"
	if len(c.blocks) > 0 {
		parent = c.blocks[len(c.blocks)-1].Hash
	}
	b := Block{
		Height:     height,
		Hash:       c.hash(height),
		ParentHash: parent,
		Transfers:  transfers,
	}
	c.blocks = append(c.blocks, b)
	return b
}

func (c *Chain) Reorg(depth int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if depth > len(c.blocks) {
		depth = len(c.blocks)
	}
	c.blocks = c.blocks[:len(c.blocks)-depth]
	c.branch++
}

func (c *Chain) hash(height uint64) string {
	return fmt.Sprintf("0x%04x%060x", c.branch, height)
}

func (c *Chain) Head(_ context.Context) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return uint64(len(c.blocks)), nil
}

func (c *Chain) BlockHash(_ context.Context, height uint64) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.BlockHashErr != nil {
		return "", false, c.BlockHashErr
	}
	if height == 0 || height > uint64(len(c.blocks)) {
		return "", false, nil
	}
	return c.blocks[height-1].Hash, true, nil
}

func (c *Chain) Block(_ context.Context, height uint64) (Block, error) {
	if c.BeforeBlock != nil {
		c.BeforeBlock(height)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if height == 0 || height > uint64(len(c.blocks)) {
		return Block{}, fmt.Errorf("chaintest: no canonical block at height %d", height)
	}
	return c.blocks[height-1], nil
}

func (c *Chain) TxByHash(_ context.Context, txHash string) (TxLocation, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.blocks {
		var transfers []Transfer
		for _, tr := range b.Transfers {
			if tr.TxHash == txHash {
				transfers = append(transfers, tr)
			}
		}
		if len(transfers) > 0 {
			return TxLocation{Height: b.Height, Hash: b.Hash, Transfers: transfers}, true, nil
		}
	}
	return TxLocation{}, false, nil
}
