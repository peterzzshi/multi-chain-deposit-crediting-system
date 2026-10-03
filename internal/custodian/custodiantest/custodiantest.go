// Package custodiantest provides a controllable mock custodian: the query
// API is always complete, while the webhook channel can duplicate,
// reorder, delay, or drop deliveries — the fault knobs the custodian
// ingest path must survive.
package custodiantest

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"deposit-crediting/internal/custodian"
)

type Custodian struct {
	mu      sync.Mutex
	truth   []custodian.Claim // the query API always returns everything
	ready   []custodian.Claim // webhook-deliverable now
	delayed []custodian.Claim // withheld until ReleaseDelayed
	vault   map[string]*big.Int
}

func New() *Custodian {
	return &Custodian{vault: make(map[string]*big.Int)}
}

// SetVaultTotal sets the vault holdings reported for a (chain, asset).
func (c *Custodian) SetVaultTotal(chain, asset string, amount *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vault[chain+":"+asset] = new(big.Int).Set(amount)
}

type ObserveOption func(*Custodian, custodian.Claim)

// Duplicate delivers the webhook twice.
func Duplicate() ObserveOption {
	return func(c *Custodian, cl custodian.Claim) { c.ready = append(c.ready, cl) }
}

// Reordered delivers the webhook before earlier pending ones.
func Reordered() ObserveOption {
	return func(c *Custodian, cl custodian.Claim) { c.ready = append([]custodian.Claim{cl}, c.ready...) }
}

// Delayed withholds the webhook until ReleaseDelayed.
func Delayed() ObserveOption {
	return func(c *Custodian, cl custodian.Claim) {
		c.delayed = append(c.delayed, cl)
		c.ready = c.ready[:len(c.ready)-1]
	}
}

// Dropped never delivers the webhook; only reconciliation can recover it.
func Dropped() ObserveOption {
	return func(c *Custodian, cl custodian.Claim) {
		c.ready = c.ready[:len(c.ready)-1]
	}
}

// Observe records a deposit: the API truth always gets it, the webhook
// channel gets it subject to the options.
func (c *Custodian) Observe(cl custodian.Claim, opts ...ObserveOption) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.truth = append(c.truth, cl)
	c.ready = append(c.ready, cl)
	for _, opt := range opts {
		opt(c, cl)
	}
}

// Webhooks drains the currently deliverable webhook claims, in delivery
// order.
func (c *Custodian) Webhooks() []custodian.Claim {
	c.mu.Lock()
	defer c.mu.Unlock()
	claims := c.ready
	c.ready = nil
	return claims
}

// ReleaseDelayed makes withheld webhooks deliverable.
func (c *Custodian) ReleaseDelayed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = append(c.ready, c.delayed...)
	c.delayed = nil
}

// FetchDeposits implements custodian.Provider: the complete truth,
// filtered by observation time.
func (c *Custodian) FetchDeposits(_ context.Context, since time.Time) ([]custodian.Claim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var claims []custodian.Claim
	for _, cl := range c.truth {
		if !cl.ObservedAt.Before(since) {
			claims = append(claims, cl)
		}
	}
	return claims, nil
}

func (c *Custodian) FetchVaultTotal(_ context.Context, chain, asset string) (*big.Int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	total, ok := c.vault[chain+":"+asset]
	if !ok {
		return nil, fmt.Errorf("custodiantest: no vault set for %s/%s", chain, asset)
	}
	return new(big.Int).Set(total), nil
}
