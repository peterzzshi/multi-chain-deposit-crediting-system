package store

import (
	"context"
	"fmt"

	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/store/ent"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/ent/ledgerentry"
)

// Deposit operations shared by the scanner and custodian stores.

func depositState(ctx context.Context, client *ent.Client, transferID string) (domain.State, error) {
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

func markReorged(ctx context.Context, client *ent.Client, transferID string, height uint64) error {
	err := client.Deposit.Update().
		Where(entdeposit.TransferID(transferID)).
		SetReorgedHeight(int64(height)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: mark reorged %s: %w", transferID, err)
	}
	return nil
}

func reincludeDeposit(ctx context.Context, client *ent.Client, transferID string, height uint64, hash string) error {
	err := client.Deposit.Update().
		Where(entdeposit.TransferID(transferID)).
		SetBlockHeight(int64(height)).
		SetBlockHash(hash).
		ClearReorgedHeight().
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: reinclude %s: %w", transferID, err)
	}
	return nil
}

func hasCredit(ctx context.Context, client *ent.Client, transferID string) (bool, error) {
	n, err := client.LedgerEntry.Query().
		Where(ledgerentry.Ref(transferID)).
		Limit(1).
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("store: credit check %s: %w", transferID, err)
	}
	return n > 0, nil
}
