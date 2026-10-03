// Package chaintest provides a controllable in-memory chain for scanner
// tests: blocks are appended explicitly and Reorg replaces the tip with a
// new branch carrying new hashes — exactly what the scanner must survive.
package chaintest

import (
	"context"
	"fmt"
	"sync"

	"deposit-crediting/internal/adapters/chain"
)

// Chain is an in-memory chain.Client. Blocks start at height 1.
type Chain struct {
	mu     sync.Mutex
	branch int
	blocks []chain.Block

	// BeforeBlock runs before each Block call, outside the lock. It exists
	// to inject mid-tick races (e.g. corrupting the chain between the
	// scanner's reorg check and its block fetch).
	BeforeBlock func(height uint64)
}

func NewChain() *Chain {
	return &Chain{}
}

// CorruptParent rewrites the recorded parent of the block at height,
// simulating chain state that no longer agrees with a scanner's cursor.
func (c *Chain) CorruptParent(height uint64, parent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocks[height-1].ParentHash = parent
}

// AddBlock appends one block with the given transfers and returns it.
func (c *Chain) AddBlock(transfers ...chain.Transfer) chain.Block {
	c.mu.Lock()
	defer c.mu.Unlock()
	height := uint64(len(c.blocks)) + 1
	parent := "0x0"
	if len(c.blocks) > 0 {
		parent = c.blocks[len(c.blocks)-1].Hash
	}
	b := chain.Block{
		Height:     height,
		Hash:       c.hash(height),
		ParentHash: parent,
		Transfers:  transfers,
	}
	c.blocks = append(c.blocks, b)
	return b
}

// Reorg orphans the last depth blocks; subsequent AddBlock calls build the
// replacement branch (with different hashes) on the fork ancestor.
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

func (c *Chain) BlockHash(_ context.Context, height uint64) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if height == 0 || height > uint64(len(c.blocks)) {
		return "", fmt.Errorf("chaintest: no canonical block at height %d", height)
	}
	return c.blocks[height-1].Hash, nil
}

func (c *Chain) Block(_ context.Context, height uint64) (chain.Block, error) {
	if c.BeforeBlock != nil {
		c.BeforeBlock(height)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if height == 0 || height > uint64(len(c.blocks)) {
		return chain.Block{}, fmt.Errorf("chaintest: no canonical block at height %d", height)
	}
	return c.blocks[height-1], nil
}

func (c *Chain) TxByHash(_ context.Context, txHash string) (chain.TxLocation, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.blocks {
		var transfers []chain.Transfer
		for _, tr := range b.Transfers {
			if tr.TxHash == txHash {
				transfers = append(transfers, tr)
			}
		}
		if len(transfers) > 0 {
			return chain.TxLocation{Height: b.Height, Hash: b.Hash, Transfers: transfers}, true, nil
		}
	}
	return chain.TxLocation{}, false, nil
}
