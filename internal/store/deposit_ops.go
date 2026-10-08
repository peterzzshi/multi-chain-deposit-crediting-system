package store

import (
	"context"
	"fmt"

	"deposit-crediting/internal/store/ent"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/ent/ledgerentry"
)

// DepositOps groups deposit-related operations shared by scanner and custodian stores.
// Embed this struct to gain access to common deposit operations without wrapper methods.
type DepositOps struct {
	client *ent.Client
}

func NewDepositOps(client *ent.Client) *DepositOps {
	return &DepositOps{client: client}
}

func (d *DepositOps) MarkReorged(ctx context.Context, transferID string, height uint64) error {
	err := d.client.Deposit.Update().
		Where(entdeposit.TransferID(transferID)).
		SetReorgedHeight(int64(height)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: mark reorged %s: %w", transferID, err)
	}
	return nil
}

func (d *DepositOps) ReincludeDeposit(ctx context.Context, transferID string, height uint64, hash string) error {
	err := d.client.Deposit.Update().
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

func (d *DepositOps) HasCredit(ctx context.Context, transferID string) (bool, error) {
	n, err := d.client.LedgerEntry.Query().
		Where(ledgerentry.Ref(transferID)).
		Limit(1).
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("store: credit check %s: %w", transferID, err)
	}
	return n > 0, nil
}
