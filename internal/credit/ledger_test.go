package credit

import (
	"errors"
	"math/big"
	"testing"

	"deposit-crediting/internal/errs"
)

func TestApplyCreditAndDebit(t *testing.T) {
	credit, err := New(Credit, "alice", "ETH", big.NewInt(100), "evm:0xabc:native")
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	balance, err := Apply(nil, credit)
	if err != nil {
		t.Fatalf("Apply(credit) unexpected error: %v", err)
	}
	if got, want := balance.String(), "100"; got != want {
		t.Fatalf("Apply(nil, credit) = %s; want %s", got, want)
	}

	debit, err := New(Debit, "alice", "ETH", big.NewInt(40), "withdrawal:1")
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
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
	debit, err := New(Debit, "alice", "ETH", big.NewInt(50), "withdrawal:1")
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if _, err := Apply(big.NewInt(40), debit); !errors.Is(err, errs.ErrInsufficientFunds) {
		t.Errorf("Apply(40, debit 50) error = %v; want errs.ErrInsufficientFunds", err)
	}
}

// ADR 0002: a user who spent a credited deposit owes the platform after a
// reorg reversal.
func TestReversalMayDriveBalanceNegative(t *testing.T) {
	credit, err := New(Credit, "alice", "ETH", big.NewInt(100), "evm:0xabc:native")
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
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
	debit, err := New(Debit, "alice", "ETH", big.NewInt(10), "withdrawal:1")
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if _, err := Reverse(debit); !errors.Is(err, errs.ErrInvalidEntry) {
		t.Errorf("Reverse(debit) error = %v; want errs.ErrInvalidEntry", err)
	}
}

// The consumer-side pattern: distinguish retryable funds problems from
// programming errors with errors.Is.
func TestErrorCategories(t *testing.T) {
	debit, err := New(Debit, "alice", "ETH", big.NewInt(50), "withdrawal:1")
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_, applyErr := Apply(big.NewInt(40), debit)
	switch {
	case errors.Is(applyErr, errs.ErrInsufficientFunds):
		// retryable: top up and try again
	case errors.Is(applyErr, errs.ErrInvalidEntry):
		t.Fatalf("Apply() = errs.ErrInvalidEntry; want errs.ErrInsufficientFunds")
	default:
		t.Fatalf("Apply() error = %v; want errs.ErrInsufficientFunds", applyErr)
	}
}

func TestReverseDerivesUniqueReference(t *testing.T) {
	credit, err := New(Credit, "alice", "ETH", big.NewInt(100), "evm:0xabc:native")
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	reversal, err := Reverse(credit)
	if err != nil {
		t.Fatalf("Reverse() unexpected error: %v", err)
	}
	if got, want := reversal.Ref, "reversal:"+credit.Ref; got != want {
		t.Errorf("reversal.Ref = %q; want %q", got, want)
	}
	if got, want := reversal.Type, Reversal; got != want {
		t.Errorf("reversal.Type = %s; want %s", got, want)
	}
	if got, want := reversal.ReversesRef, credit.Ref; got != want {
		t.Errorf("reversal.ReversesRef = %q; want %q", got, want)
	}
}

func TestNonReversalEntriesHaveNoReversesRef(t *testing.T) {
	for _, typ := range []TransactionType{Credit, Debit} {
		entry, err := New(typ, "alice", "ETH", big.NewInt(100), "ref")
		if err != nil {
			t.Fatalf("New(%s) unexpected error: %v", typ, err)
		}
		if entry.ReversesRef != "" {
			t.Errorf("New(%s).ReversesRef = %q; want empty", typ, entry.ReversesRef)
		}
	}
}

func TestApplyDoesNotMutateOperands(t *testing.T) {
	balance := big.NewInt(100)
	credit, err := New(Credit, "alice", "ETH", big.NewInt(1), "evm:0xabc:native")
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if _, err := Apply(balance, credit); err != nil {
		t.Fatalf("Apply() unexpected error: %v", err)
	}
	if got, want := balance.String(), "100"; got != want {
		t.Errorf("Apply mutated balance to %s; want it unchanged at %s", got, want)
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		typ     TransactionType
		account string
		asset   string
		amount  *big.Int
		ref     string
	}{
		{"unknown type", "interest", "alice", "ETH", big.NewInt(1), "ref"},
		{"empty type", "", "alice", "ETH", big.NewInt(1), "ref"},
		{"empty account", Credit, "", "ETH", big.NewInt(1), "ref"},
		{"empty asset", Credit, "alice", "", big.NewInt(1), "ref"},
		{"empty ref", Credit, "alice", "ETH", big.NewInt(1), ""},
		{"zero amount", Credit, "alice", "ETH", big.NewInt(0), "ref"},
		{"negative amount", Credit, "alice", "ETH", big.NewInt(-5), "ref"},
		{"nil amount", Credit, "alice", "ETH", nil, "ref"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.typ, tt.account, tt.asset, tt.amount, tt.ref); !errors.Is(err, errs.ErrInvalidEntry) {
				t.Errorf("New(%s, %q, %q, %v, %q) error = %v; want errs.ErrInvalidEntry",
					tt.typ, tt.account, tt.asset, tt.amount, tt.ref, err)
			}
		})
	}
}
