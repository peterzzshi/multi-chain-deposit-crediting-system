package store

import (
	"context"
	"fmt"

	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/store/ent"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
)

// queryTrackedDeposits builds a query for deposits in Pending or Credited state.
// Parameterized by mode to avoid duplication between scanner and custodian stores.
func queryTrackedDeposits(client *ent.Client, chainID string, mode domain.Mode) *ent.DepositQuery {
	return client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.ModeEQ(mode),
			entdeposit.StateIn(domain.StatePending, domain.StateCredited),
		)
}

// queryReorgedDeposits builds a query for deposits in the given reorg-related states.
// Parameterized by mode and states to support different filtering needs.
func queryReorgedDeposits(client *ent.Client, chainID string, mode domain.Mode, states ...domain.State) *ent.DepositQuery {
	return client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.ModeEQ(mode),
			entdeposit.StateIn(states...),
		)
}

// execOpenDeposit handles the common OpenDeposit execution pattern:
// create the deposit, and on constraint error, return the existing state.
// This eliminates the need for a separate getState method.
func execOpenDeposit(ctx context.Context, create *ent.DepositCreate, transferID string, client *ent.Client) (domain.State, error) {
	err := create.Exec(ctx)
	if ent.IsConstraintError(err) {
		// Deposit already exists; return its current state.
		row, err := client.Deposit.Query().
			Where(entdeposit.TransferID(transferID)).
			Only(ctx)
		if ent.IsNotFound(err) {
			return "", errs.ErrDepositNotFound
		}
		if err != nil {
			return "", fmt.Errorf("store: deposit state %s: %w", transferID, err)
		}
		return domain.State(row.State), nil
	}
	if err != nil {
		return domain.StateCreated, fmt.Errorf("store: open deposit %s: %w", transferID, err)
	}
	return domain.StateCreated, nil
}
