package adapters_test

import (
	"testing"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/domain"
)

func TestLogicalIDMapsKinds(t *testing.T) {
	tests := []struct {
		name string
		tr   adapters.Transfer
		want string
	}{
		{"token", adapters.Transfer{Kind: adapters.Token, TxHash: "0xabc", Asset: "0xcontract", LogIndex: 2}, "evm:0xabc:token:0xcontract:2"},
		{"native", adapters.Transfer{Kind: adapters.Native, TxHash: "0xabc"}, "evm:0xabc:native"},
		{"internal", adapters.Transfer{Kind: adapters.Internal, TxHash: "0xabc", TraceIndex: 1}, "evm:0xabc:trace:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := tt.tr.LogicalID(domain.NetworkID("evm"))
			if err != nil {
				t.Fatalf("LogicalID() unexpected error: %v", err)
			}
			if got := id.String(); got != tt.want {
				t.Errorf("LogicalID() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestLogicalIDRejectsUnknownKind(t *testing.T) {
	tr := adapters.Transfer{Kind: "mystery", TxHash: "0xabc"}
	if _, err := tr.LogicalID(domain.NetworkID("evm")); err == nil {
		t.Error("LogicalID() = nil error for unknown kind; want error")
	}
}
