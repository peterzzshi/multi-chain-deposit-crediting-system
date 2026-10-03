package scanner

import (
	"math/big"
	"testing"

	"deposit-crediting/internal/adapters/chain"
)

func TestLogicalIDMapsKinds(t *testing.T) {
	tests := []struct {
		name string
		tr   chain.Transfer
		want string
	}{
		{"token", chain.Transfer{Kind: chain.Token, TxHash: "0xabc", Asset: "0xcontract", LogIndex: 2}, "evm:0xabc:token:0xcontract:2"},
		{"native", chain.Transfer{Kind: chain.Native, TxHash: "0xabc"}, "evm:0xabc:native"},
		{"internal", chain.Transfer{Kind: chain.Internal, TxHash: "0xabc", TraceIndex: 1}, "evm:0xabc:trace:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := logicalID("evm", tt.tr)
			if err != nil {
				t.Fatalf("logicalID() unexpected error: %v", err)
			}
			if got := id.String(); got != tt.want {
				t.Errorf("logicalID() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestTransferTargetsDedupesAndSkips(t *testing.T) {
	got := transferTargets([]chain.Transfer{
		{To: "0xa", Amount: big.NewInt(1)},
		{To: "0xa", Amount: big.NewInt(2)},
		{To: "0xb", Amount: big.NewInt(3)},
		{To: "", Amount: big.NewInt(4)},
		{To: "0xc", Amount: nil},
	})
	if len(got) != 2 || got[0] != "0xa" || got[1] != "0xb" {
		t.Errorf("transferTargets() = %v; want [0xa 0xb]", got)
	}
}
