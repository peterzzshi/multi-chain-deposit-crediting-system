// Package custodianmock is a stand-in custodian provider for local runs and
// tests. It is not part of the production path: it backs cmd/custodian-api.
// The fault options let a claim be delivered twice, out of order, late, or
// not at all, so the reconciler's dedup and lag handling can be exercised.
package custodianmock

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"deposit-crediting/internal/custodian"
)

type MockCustodian struct {
	mu      sync.Mutex
	truth   []custodian.Claim // the query API always returns everything
	ready   []custodian.Claim // webhook-deliverable now
	delayed []custodian.Claim // withheld until ReleaseDelayed
	vault   map[string]*big.Int
}

func NewMock() *MockCustodian {
	return &MockCustodian{vault: make(map[string]*big.Int)}
}

func (c *MockCustodian) SetVaultTotal(chain, asset string, amount *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vault[chain+":"+asset] = new(big.Int).Set(amount)
}

// delivery accumulates the chosen fates for one observed claim. Options
// describe what should happen to the claim being observed, so two options can
// never collide over the delivery queue.
type delivery struct {
	copies  int  // extra webhook copies beyond the first
	delayed bool // withhold until ReleaseDelayed
	dropped bool // omit from the webhook channel entirely
	reorder bool // deliver ahead of anything already queued
}

type ObserveOption func(*delivery)

// Duplicate delivers the claim's webhook copy an extra time.
func Duplicate() ObserveOption {
	return func(d *delivery) { d.copies++ }
}

// Reordered delivers the claim's webhook copy ahead of the queued backlog.
func Reordered() ObserveOption {
	return func(d *delivery) { d.reorder = true }
}

// Delayed withholds the claim's webhook copy until ReleaseDelayed.
func Delayed() ObserveOption {
	return func(d *delivery) { d.delayed = true }
}

// Dropped omits the claim's webhook copy; the query API still returns it.
func Dropped() ObserveOption {
	return func(d *delivery) { d.dropped = true }
}

// Observe records a deposit: the API truth always gets it, the webhook
// channel gets it subject to the options.
func (c *MockCustodian) Observe(cl custodian.Claim, opts ...ObserveOption) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.truth = append(c.truth, cl)

	var d delivery
	for _, opt := range opts {
		opt(&d)
	}
	switch {
	case d.dropped:
		return
	case d.delayed:
		c.delayed = append(c.delayed, cl)
		return
	case d.reorder:
		c.ready = append([]custodian.Claim{cl}, c.ready...)
	default:
		c.ready = append(c.ready, cl)
	}
	for range d.copies {
		c.ready = append(c.ready, cl)
	}
}

func (c *MockCustodian) Webhooks() []custodian.Claim {
	c.mu.Lock()
	defer c.mu.Unlock()
	claims := c.ready
	c.ready = nil
	return claims
}

func (c *MockCustodian) ReleaseDelayed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = append(c.ready, c.delayed...)
	c.delayed = nil
}

func (c *MockCustodian) FetchDeposits(_ context.Context, since time.Time) ([]custodian.Claim, error) {
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

func (c *MockCustodian) FetchVaultTotal(_ context.Context, chain, asset string) (*big.Int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	total, ok := c.vault[chain+":"+asset]
	if !ok {
		return nil, fmt.Errorf("mock custodian: no vault set for %s/%s", chain, asset)
	}
	return new(big.Int).Set(total), nil
}
