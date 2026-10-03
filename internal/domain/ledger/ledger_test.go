package ledger

import (
	"errors"
	"math/big"
	"testing"
)

func TestApplyCreditAndDebit(t *testing.T) {
	credit, err := NewCredit("alice", "ETH", big.NewInt(100), "evm:0xabc:native")
	if err != nil {
		t.Fatalf("NewCredit() unexpected error: %v", err)
	}
	balance, err := Apply(nil, credit)
	if err != nil {
		t.Fatalf("Apply(credit) unexpected error: %v", err)
	}
	if got, want := balance.String(), "100"; got != want {
		t.Fatalf("Apply(nil, credit) = %s; want %s", got, want)
	}

	debit, err := NewDebit("alice", "ETH", big.NewInt(40), "withdrawal:1")
	if err != nil {
		t.Fatalf("NewDebit() unexpected error: %v", err)
	}
	balance, err = Apply(balance, debit)
	if err != nil {
		t.Fatalf("Apply(debit) unexpected error: %v", err)
	}
	if got, want := balance.String(), "60"; got != want {
		t.Errorf("Apply(100, debit 40) = %s; want %s", got, want)
	}
}

func TestApplyDebitRejectsInsufficientFunds(t *testing.T) {
	debit, err := NewDebit("alice", "ETH", big.NewInt(50), "withdrawal:1")
	if err != nil {
		t.Fatalf("NewDebit() unexpected error: %v", err)
	}
	if _, err := Apply(big.NewInt(40), debit); !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("Apply(40, debit 50) error = %v; want ErrInsufficientFunds", err)
	}
}

// TestReversalMayDriveBalanceNegative pins the ADR 0002 rule: a user who
// spent a credited deposit owes the platform after a reorg reversal.
func TestReversalMayDriveBalanceNegative(t *testing.T) {
	credit, err := NewCredit("alice", "ETH", big.NewInt(100), "evm:0xabc:native")
	if err != nil {
		t.Fatalf("NewCredit() unexpected error: %v", err)
	}
	reversal, err := Reverse(credit)
	if err != nil {
		t.Fatalf("Reverse() unexpected error: %v", err)
	}
	balance, err := Apply(big.NewInt(30), reversal)
	if err != nil {
		t.Fatalf("Apply(reversal) unexpected error: %v", err)
	}
	if got, want := balance.String(), "-70"; got != want {
		t.Errorf("Apply(30, reversal 100) = %s; want %s", got, want)
	}
	if balance.Sign() >= 0 {
		t.Error("balance.Sign() >= 0 after reversal; want negative (account must be flagged)")
	}
}

func TestReverseOnlyAppliesToCredits(t *testing.T) {
	debit, err := NewDebit("alice", "ETH", big.NewInt(10), "withdrawal:1")
	if err != nil {
		t.Fatalf("NewDebit() unexpected error: %v", err)
	}
	if _, err := Reverse(debit); !errors.Is(err, ErrInvalidEntry) {
		t.Errorf("Reverse(debit) error = %v; want ErrInvalidEntry", err)
	}
}

// TestErrorCategories exercises the consumer-side pattern: distinguish
// retryable funds problems from programming errors with errors.Is.
func TestErrorCategories(t *testing.T) {
	debit, err := NewDebit("alice", "ETH", big.NewInt(50), "withdrawal:1")
	if err != nil {
		t.Fatalf("NewDebit() unexpected error: %v", err)
	}
	_, applyErr := Apply(big.NewInt(40), debit)
	switch {
	case errors.Is(applyErr, ErrInsufficientFunds):
		// retryable: top up and try again
	case errors.Is(applyErr, ErrInvalidEntry):
		t.Fatalf("Apply() = ErrInvalidEntry; want ErrInsufficientFunds")
	default:
		t.Fatalf("Apply() error = %v; want ErrInsufficientFunds", applyErr)
	}
}

func TestReversePreservesReference(t *testing.T) {
	credit, err := NewCredit("alice", "ETH", big.NewInt(100), "evm:0xabc:native")
	if err != nil {
		t.Fatalf("NewCredit() unexpected error: %v", err)
	}
	reversal, err := Reverse(credit)
	if err != nil {
		t.Fatalf("Reverse() unexpected error: %v", err)
	}
	if got, want := reversal.Ref, credit.Ref; got != want {
		t.Errorf("reversal.Ref = %q; want %q (original credit ref)", got, want)
	}
	if got, want := reversal.Type, TypeReversal; got != want {
		t.Errorf("reversal.Type = %s; want %s", got, want)
	}
}

func TestApplyDoesNotMutateOperands(t *testing.T) {
	balance := big.NewInt(100)
	credit, err := NewCredit("alice", "ETH", big.NewInt(1), "evm:0xabc:native")
	if err != nil {
		t.Fatalf("NewCredit() unexpected error: %v", err)
	}
	if _, err := Apply(balance, credit); err != nil {
		t.Fatalf("Apply() unexpected error: %v", err)
	}
	if got, want := balance.String(), "100"; got != want {
		t.Errorf("Apply mutated balance to %s; want it unchanged at %s", got, want)
	}
}

func TestNewEntryRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		account string
		asset   string
		amount  *big.Int
		ref     string
	}{
		{"empty account", "", "ETH", big.NewInt(1), "ref"},
		{"empty asset", "alice", "", big.NewInt(1), "ref"},
		{"empty ref", "alice", "ETH", big.NewInt(1), ""},
		{"zero amount", "alice", "ETH", big.NewInt(0), "ref"},
		{"negative amount", "alice", "ETH", big.NewInt(-5), "ref"},
		{"nil amount", "alice", "ETH", nil, "ref"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewCredit(tt.account, tt.asset, tt.amount, tt.ref); !errors.Is(err, ErrInvalidEntry) {
				t.Errorf("NewCredit(%q, %q, %v, %q) error = %v; want ErrInvalidEntry",
					tt.account, tt.asset, tt.amount, tt.ref, err)
			}
		})
	}
}
