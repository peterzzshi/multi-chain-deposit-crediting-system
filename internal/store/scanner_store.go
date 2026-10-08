package store

import (
	"context"
	"fmt"

	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/scanner"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/ent/canonicalblock"
	"deposit-crediting/internal/store/ent/chaincursor"
	"deposit-crediting/internal/store/ent/depositaddress"
)

// ScannerStore implements scanner.Store over the ent client.
type ScannerStore struct {
	client *ent.Client
	*DepositOps
}

func NewScannerStore(client *ent.Client) *ScannerStore {
	return &ScannerStore{
		client:     client,
		DepositOps: NewDepositOps(client),
	}
}

func (s *ScannerStore) Cursor(ctx context.Context, chainID string) (scanner.Cursor, bool, error) {
	row, err := s.client.ChainCursor.Query().Where(chaincursor.Chain(chainID)).Only(ctx)
	if ent.IsNotFound(err) {
		return scanner.Cursor{}, false, nil
	}
	if err != nil {
		return scanner.Cursor{}, false, fmt.Errorf("store: cursor %s: %w", chainID, err)
	}
	return scanner.Cursor{Height: uint64(row.Height), Hash: row.Hash}, true, nil
}

func (s *ScannerStore) SaveCursor(ctx context.Context, chainID string, c scanner.Cursor) error {
	n, err := s.client.ChainCursor.Update().
		Where(chaincursor.Chain(chainID)).
		SetHeight(int64(c.Height)).
		SetHash(c.Hash).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store: save cursor %s: %w", chainID, err)
	}
	if n == 0 {
		err = s.client.ChainCursor.Create().
			SetChain(chainID).
			SetHeight(int64(c.Height)).
			SetHash(c.Hash).
			Exec(ctx)
		if err != nil && !ent.IsConstraintError(err) {
			return fmt.Errorf("store: create cursor %s: %w", chainID, err)
		}
	}
	return nil
}

func (s *ScannerStore) RecordBlock(ctx context.Context, chainID string, height uint64, hash string) error {
	err := s.client.CanonicalBlock.Create().
		SetChain(chainID).
		SetHeight(int64(height)).
		SetHash(hash).
		Exec(ctx)
	if err != nil && !ent.IsConstraintError(err) {
		return fmt.Errorf("store: record block %s/%d: %w", chainID, height, err)
	}
	return nil
}

func (s *ScannerStore) BlockHashAt(ctx context.Context, chainID string, height uint64) (string, error) {
	row, err := s.client.CanonicalBlock.Query().
		Where(canonicalblock.Chain(chainID), canonicalblock.Height(int64(height))).
		Only(ctx)
	if ent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: block hash %s/%d: %w", chainID, height, err)
	}
	return row.Hash, nil
}

func (s *ScannerStore) DropBlocksAbove(ctx context.Context, chainID string, height uint64) error {
	_, err := s.client.CanonicalBlock.Delete().
		Where(canonicalblock.Chain(chainID), canonicalblock.HeightGT(int64(height))).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: drop blocks %s above %d: %w", chainID, height, err)
	}
	return nil
}

func (s *ScannerStore) ResolveRecipients(ctx context.Context, chainID string, addrs []string) (map[string]string, error) {
	if len(addrs) == 0 {
		return nil, nil
	}
	rows, err := s.client.DepositAddress.Query().
		Where(
			depositaddress.Chain(chainID),
			depositaddress.AddressIn(addrs...),
			depositaddress.Active(true),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: resolve recipients %s: %w", chainID, err)
	}
	recipients := make(map[string]string, len(rows))
	for _, r := range rows {
		recipients[r.Address] = r.Account
	}
	return recipients, nil
}

func (s *ScannerStore) OpenDeposit(ctx context.Context, p domain.OpenDepositParams) (domain.State, error) {
	create := s.client.Deposit.Create().
		SetTransferID(p.TransferID).
		SetChain(p.Chain).
		SetAsset(p.Asset).
		SetAccount(p.Account).
		SetAddress(p.Address).
		SetAmount(p.Amount.String()).
		SetMode(domain.ModeSelfBuilt).
		SetState(domain.StateCreated).
		SetBlockHeight(int64(p.Height)).
		SetBlockHash(p.Hash).
		SetTxHash(p.TxHash)

	return execOpenDeposit(ctx, create, p.TransferID, s.client)
}

func (s *ScannerStore) TrackedDeposits(ctx context.Context, chainID string) ([]domain.TrackedDeposit, error) {
	rows, err := queryTrackedDeposits(s.client, chainID, domain.ModeSelfBuilt).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: tracked deposits %s: %w", chainID, err)
	}
	var tracked []domain.TrackedDeposit
	for _, r := range rows {
		if r.BlockHeight == nil {
			continue
		}
		tracked = append(tracked, domain.TrackedDeposit{
			TransferID: r.TransferID,
			Asset:      r.Asset,
			State:      r.State,
			Height:     uint64(*r.BlockHeight),
		})
	}
	return tracked, nil
}

func (s *ScannerStore) ReorgedDeposits(ctx context.Context, chainID string) ([]domain.ReorgedDeposit, error) {
	rows, err := queryReorgedDeposits(s.client, chainID, domain.ModeSelfBuilt, domain.StateReorged).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: reorged deposits %s: %w", chainID, err)
	}
	var reorged []domain.ReorgedDeposit
	for _, r := range rows {
		entry := domain.ReorgedDeposit{TransferID: r.TransferID, Asset: r.Asset, State: r.State}
		if r.TxHash != nil {
			entry.TxHash = *r.TxHash
		}
		if r.ReorgedHeight != nil {
			h := uint64(*r.ReorgedHeight)
			entry.Height = &h
		}
		reorged = append(reorged, entry)
	}
	return reorged, nil
}
