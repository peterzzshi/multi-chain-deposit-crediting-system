package store

import (
	"context"
	"fmt"
	"math/big"

	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/scanner"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/ent/assetconfig"
	"deposit-crediting/internal/store/ent/canonicalblock"
	"deposit-crediting/internal/store/ent/chaincursor"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/ent/depositaddress"
	"deposit-crediting/internal/store/ent/ledgerentry"
)

// ScannerStore implements scanner.Store over the ent client.
type ScannerStore struct {
	client *ent.Client
}

func NewScannerStore(client *ent.Client) *ScannerStore {
	return &ScannerStore{client: client}
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

func (s *ScannerStore) AssetConfigs(ctx context.Context, chainID string) ([]scanner.AssetConfig, error) {
	rows, err := s.client.AssetConfig.Query().
		Where(assetconfig.Chain(chainID), assetconfig.ModeEQ(assetconfig.ModeSelfBuilt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: asset configs %s: %w", chainID, err)
	}
	configs := make([]scanner.AssetConfig, 0, len(rows))
	for _, r := range rows {
		min, ok := new(big.Int).SetString(r.MinAmount, 10)
		if !ok {
			return nil, fmt.Errorf("store: corrupt min amount %q for %s/%s", r.MinAmount, chainID, r.Asset)
		}
		configs = append(configs, scanner.AssetConfig{
			Asset:       r.Asset,
			MinAmount:   min,
			NCredit:     uint64(r.NCredit),
			NFinalize:   uint64(r.NFinalize),
			ReorgWindow: uint64(r.ReorgWindow),
		})
	}
	return configs, nil
}

func (s *ScannerStore) ResolveRecipients(ctx context.Context, chainID string, addrs []string) (map[string]string, error) {
	if len(addrs) == 0 {
		return nil, nil
	}
	rows, err := s.client.DepositAddress.Query().
		Where(
			depositaddress.Chain(chainID),
			depositaddress.AddressIn(addrs...),
			depositaddress.ModeEQ(depositaddress.ModeSelfBuilt),
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

func (s *ScannerStore) OpenDeposit(ctx context.Context, p scanner.OpenParams) (bool, error) {
	err := s.client.Deposit.Create().
		SetTransferID(p.TransferID).
		SetChain(p.Chain).
		SetAsset(p.Asset).
		SetAccount(p.Account).
		SetAddress(p.Address).
		SetAmount(p.Amount.String()).
		SetMode(entdeposit.ModeSelfBuilt).
		SetState(entdeposit.State(p.State)).
		SetBlockHeight(int64(p.Height)).
		SetBlockHash(p.Hash).
		Exec(ctx)
	if ent.IsConstraintError(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: open deposit %s: %w", p.TransferID, err)
	}
	return true, nil
}

func (s *ScannerStore) ReincludeDeposit(ctx context.Context, transferID string, height uint64, hash string) error {
	err := s.client.Deposit.Update().
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

func (s *ScannerStore) DepositState(ctx context.Context, transferID string) (deposit.State, error) {
	row, err := s.client.Deposit.Query().
		Where(entdeposit.TransferID(transferID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return "", errs.ErrDepositNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: deposit state %s: %w", transferID, err)
	}
	return deposit.State(row.State), nil
}

func (s *ScannerStore) TrackedDeposits(ctx context.Context, chainID string) ([]scanner.Tracked, error) {
	rows, err := s.client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.ModeEQ(entdeposit.ModeSelfBuilt),
			entdeposit.StateIn(entdeposit.StatePENDING, entdeposit.StateCREDITED),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: tracked deposits %s: %w", chainID, err)
	}
	var tracked []scanner.Tracked
	for _, r := range rows {
		if r.BlockHeight == nil {
			continue
		}
		tracked = append(tracked, scanner.Tracked{
			TransferID: r.TransferID,
			Asset:      r.Asset,
			State:      deposit.State(r.State),
			Height:     uint64(*r.BlockHeight),
		})
	}
	return tracked, nil
}

func (s *ScannerStore) ReorgedDeposits(ctx context.Context, chainID string) ([]scanner.Reorged, error) {
	rows, err := s.client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.ModeEQ(entdeposit.ModeSelfBuilt),
			entdeposit.StateEQ(entdeposit.StateREORGED),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: reorged deposits %s: %w", chainID, err)
	}
	var reorged []scanner.Reorged
	for _, r := range rows {
		entry := scanner.Reorged{TransferID: r.TransferID, Asset: r.Asset}
		if r.ReorgedHeight != nil {
			h := uint64(*r.ReorgedHeight)
			entry.Height = &h
		}
		reorged = append(reorged, entry)
	}
	return reorged, nil
}

func (s *ScannerStore) MarkReorged(ctx context.Context, transferID string, height uint64) error {
	err := s.client.Deposit.Update().
		Where(entdeposit.TransferID(transferID)).
		SetReorgedHeight(int64(height)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: mark reorged %s: %w", transferID, err)
	}
	return nil
}

func (s *ScannerStore) HasCredit(ctx context.Context, transferID string) (bool, error) {
	n, err := s.client.LedgerEntry.Query().
		Where(ledgerentry.Ref(transferID)).
		Limit(1).
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("store: credit check %s: %w", transferID, err)
	}
	return n > 0, nil
}
