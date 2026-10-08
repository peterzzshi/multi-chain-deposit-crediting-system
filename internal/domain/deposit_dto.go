package domain

import "math/big"

// OpenDepositParams contains the fields needed to create a new deposit record.
type OpenDepositParams struct {
	TransferID  string
	State       State
	Chain       string
	Asset       string
	Account     string
	Address     string
	Amount      *big.Int
	Height      uint64
	Hash        string
	TxHash      string
	SourceEvent string // empty for self-built mode
}

// TrackedDeposit represents a deposit in a non-terminal state that needs
// confirmation depth or finality processing.
type TrackedDeposit struct {
	TransferID string
	Asset      string
	State      State
	Height     uint64
	BlockHash  string // only populated for custodian mode
}

// ReorgedDeposit represents a deposit that was reorged out and may be
// reincluded or expired.
type ReorgedDeposit struct {
	TransferID string
	Asset      string
	State      State
	TxHash     string  // for custodian rechecker to locate reinclusion
	Height     *uint64 // reorg marker height; nil means needs repair
}
