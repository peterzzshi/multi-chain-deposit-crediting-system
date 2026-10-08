package adapters

import (
	"errors"
	"testing"

	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
)

func TestSourceEventIDString(t *testing.T) {
	id, err := NewSourceEventID("fireblocks", "evt_123")
	if err != nil {
		t.Fatalf("NewSourceEventID() unexpected error: %v", err)
	}
	if got, want := id.String(), "fireblocks:evt_123"; got != want {
		t.Errorf("String() = %q; want %q", got, want)
	}
}

func TestNewSourceEventIDRejectsBadParts(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		eventID  string
	}{
		{"empty provider", "", "evt_1"},
		{"empty event ID", "fireblocks", ""},
		{"separator in event ID", "fireblocks", "evt:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewSourceEventID(tt.provider, tt.eventID); !errors.Is(err, errs.ErrInvalidPart) {
				t.Errorf("NewSourceEventID(%q, %q) error = %v; want errs.ErrInvalidPart", tt.provider, tt.eventID, err)
			}
		})
	}
}

func TestLogicalTransferIDCanonicalForms(t *testing.T) {
	token, err := NewTokenTransfer("evm", "0xabc", "0xdef", 12)
	if err != nil {
		t.Fatalf("NewTokenTransfer() unexpected error: %v", err)
	}
	native, err := NewNativeTransfer("evm", "0xabc")
	if err != nil {
		t.Fatalf("NewNativeTransfer() unexpected error: %v", err)
	}
	internal, err := NewInternalNativeTransfer("fast", "0x999", 3)
	if err != nil {
		t.Fatalf("NewInternalNativeTransfer() unexpected error: %v", err)
	}

	tests := []struct {
		name string
		id   LogicalTransferID
		want string
	}{
		{"token", token, "evm:0xabc:token:0xdef:12"},
		{"native", native, "evm:0xabc:native"},
		{"internal native", internal, "fast:0x999:trace:3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.String(); got != tt.want {
				t.Errorf("String() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestTokenTransferIdentityExcludesBlockHash(t *testing.T) {
	// The same transfer observed in two competing blocks must produce one
	// identity, so re-inclusion after a reorg is a no-op (ADR 0001).
	a, err := NewTokenTransfer("evm", "0xabc", "0xdef", 12)
	if err != nil {
		t.Fatalf("NewTokenTransfer() unexpected error: %v", err)
	}
	b, err := NewTokenTransfer("evm", "0xabc", "0xdef", 12)
	if err != nil {
		t.Fatalf("NewTokenTransfer() unexpected error: %v", err)
	}
	if a.String() != b.String() {
		t.Errorf("same chain facts gave %q and %q; want identical IDs", a.String(), b.String())
	}
}

func TestNewTokenTransferRejectsBadInput(t *testing.T) {
	tests := []struct {
		name     string
		chain    string
		txHash   string
		contract string
		logIndex int
	}{
		{"empty chain", "", "0xabc", "0xdef", 0},
		{"empty tx hash", "evm", "", "0xdef", 0},
		{"empty contract", "evm", "0xabc", "", 0},
		{"negative log index", "evm", "0xabc", "0xdef", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewTokenTransfer(domain.NetworkID(tt.chain), tt.txHash, tt.contract, tt.logIndex); !errors.Is(err, errs.ErrInvalidPart) {
				t.Errorf("NewTokenTransfer(%q, %q, %q, %d) error = %v; want errs.ErrInvalidPart",
					tt.chain, tt.txHash, tt.contract, tt.logIndex, err)
			}
		})
	}
}

func TestNewInternalNativeTransferRejectsNegativeTraceIndex(t *testing.T) {
	if _, err := NewInternalNativeTransfer("evm", "0xabc", -1); !errors.Is(err, errs.ErrInvalidPart) {
		t.Errorf("NewInternalNativeTransfer() error = %v for negative trace index; want errs.ErrInvalidPart", err)
	}
}
