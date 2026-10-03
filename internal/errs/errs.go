// Package errs holds the sentinel errors matched with errors.Is across the
// codebase. Each is wrapped with %w at the failure site.
package errs

import "errors"

var (
	ErrInvalidPart       = errors.New("invalid ID part")
	ErrIllegalTransition = errors.New("illegal deposit transition")
	ErrInvalidEntry      = errors.New("invalid ledger entry")
	ErrInsufficientFunds = errors.New("insufficient funds") // retryable after top-up (ADR 0003)
	ErrDepositNotFound   = errors.New("deposit not found")
	ErrAccountFlagged    = errors.New("account flagged")      // debits blocked until the reversal shortfall is resolved (ADR 0002)
	ErrDuplicateRef      = errors.New("duplicate ledger ref") // idempotency boundary; usually treat as success (ADR 0001)

	// ErrChainInconsistent means chain data contradicts the recorded cursor
	// beyond what reorg rewind handles (e.g. a reorg landed mid-tick).
	// Retry cannot fix it — the operator must repair the cursor.
	ErrChainInconsistent = errors.New("chain inconsistent with cursor")
)
