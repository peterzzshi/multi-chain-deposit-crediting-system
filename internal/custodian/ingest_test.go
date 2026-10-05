package custodian

import (
	"math/big"
	"testing"

	"deposit-crediting/internal/adapters"
)

func TestMatchTransfer(t *testing.T) {
	two, three := 2, 3
	logs := []adapters.Transfer{
		{Kind: adapters.Token, TxHash: "0xtx", To: "0xaaa", Asset: "0xtoken", Amount: big.NewInt(100), LogIndex: 2},
		{Kind: adapters.Token, TxHash: "0xtx", To: "0xaaa", Asset: "0xtoken", Amount: big.NewInt(100), LogIndex: 3},
	}
	native := []adapters.Transfer{
		{Kind: adapters.Native, TxHash: "0xtx", To: "0xaaa", Asset: "ETH", Amount: big.NewInt(100)},
	}
	claim := Claim{TxHash: "0xtx", To: "0xaaa", Asset: "0xtoken", Amount: big.NewInt(100)}

	tests := []struct {
		name      string
		transfers []adapters.Transfer
		claim     Claim
		wantOK    bool
		wantLog   int
	}{
		{"unique match without discriminator", native,
			Claim{TxHash: "0xtx", To: "0xaaa", Asset: "ETH", Amount: big.NewInt(100)}, true, 0},
		{"ambiguous without discriminator rejected", logs, claim, false, 0},
		{"log index disambiguates", logs,
			Claim{TxHash: "0xtx", To: "0xaaa", Asset: "0xtoken", Amount: big.NewInt(100), Kind: adapters.Token, LogIndex: &three}, true, 3},
		{"wrong log index rejected", logs,
			Claim{TxHash: "0xtx", To: "0xaaa", Asset: "0xtoken", Amount: big.NewInt(100), LogIndex: &two, TraceIndex: &three}, false, 0},
		{"amount mismatch rejected", native,
			Claim{TxHash: "0xtx", To: "0xaaa", Asset: "ETH", Amount: big.NewInt(50)}, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, ok := matchTransfer(tt.transfers, tt.claim)
			if ok != tt.wantOK {
				t.Fatalf("matchTransfer() ok = %v; want %v", ok, tt.wantOK)
			}
			if ok && tr.LogIndex != tt.wantLog {
				t.Errorf("matched log index = %d; want %d", tr.LogIndex, tt.wantLog)
			}
		})
	}
}
