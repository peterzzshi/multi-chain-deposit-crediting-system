package errs

import "errors"

var (
	ErrInvalidPart       = errors.New("invalid ID part")
	ErrIllegalTransition = errors.New("illegal deposit transition")
	ErrInvalidEntry      = errors.New("invalid ledger entry")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrDepositNotFound   = errors.New("deposit not found")
	ErrAccountFlagged    = errors.New("account flagged")
	ErrDuplicateRef      = errors.New("duplicate ledger ref")
	ErrChainInconsistent = errors.New("chain inconsistent with cursor")
	ErrClaimNotOnChain   = errors.New("claim not on chain")
)
