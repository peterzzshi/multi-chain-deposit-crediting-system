package scanner

import (
	"math/big"
	"testing"

	"deposit-crediting/internal/adapters"
)

func TestTransferTargetsDedupesAndSkips(t *testing.T) {
	got := transferTargets([]adapters.Transfer{
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
