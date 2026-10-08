package domain

import "math/big"

type Mode string

const (
	ModeSelfBuilt Mode = "self_built"
	ModeCustodian Mode = "custodian"
)

// AssetPolicy defines risk and confirmation parameters for one asset on one chain.
type AssetPolicy struct {
	Asset       string
	MinAmount   *big.Int
	NCredit     uint64   // confirmation depth for crediting
	NFinalize   uint64   // finality depth
	ReorgWindow uint64   // blocks to wait before expiring a reorged deposit
	ExposureCap *big.Int // nil = unlimited
}
