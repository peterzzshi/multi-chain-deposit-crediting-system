package custodian

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"
)

type MockCustodian struct {
	mu      sync.Mutex
	truth   []Claim // the query API always returns everything
	ready   []Claim // webhook-deliverable now
	delayed []Claim // withheld until ReleaseDelayed
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

type ObserveOption func(*MockCustodian, Claim)

func Duplicate() ObserveOption {
	return func(c *MockCustodian, cl Claim) { c.ready = append(c.ready, cl) }
}

func Reordered() ObserveOption {
	return func(c *MockCustodian, cl Claim) { c.ready = append([]Claim{cl}, c.ready...) }
}

func Delayed() ObserveOption {
	return func(c *MockCustodian, cl Claim) {
		c.delayed = append(c.delayed, cl)
		c.ready = c.ready[:len(c.ready)-1]
	}
}

func Dropped() ObserveOption {
	return func(c *MockCustodian, cl Claim) {
		c.ready = c.ready[:len(c.ready)-1]
	}
}

// Observe records a deposit: the API truth always gets it, the webhook
// channel gets it subject to the options.
func (c *MockCustodian) Observe(cl Claim, opts ...ObserveOption) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.truth = append(c.truth, cl)
	c.ready = append(c.ready, cl)
	for _, opt := range opts {
		opt(c, cl)
	}
}

func (c *MockCustodian) Webhooks() []Claim {
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

func (c *MockCustodian) FetchDeposits(_ context.Context, since time.Time) ([]Claim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var claims []Claim
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
