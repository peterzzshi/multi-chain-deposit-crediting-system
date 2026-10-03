package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"deposit-crediting/internal/custodian"
	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/ent/accountbalance"
	"deposit-crediting/internal/store/ent/assetconfig"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/ent/depositaddress"
	"deposit-crediting/internal/store/ent/sourceevent"
)

// CustodianStore implements custodian.Store over the ent client.
type CustodianStore struct {
	client *ent.Client
}

func NewCustodianStore(client *ent.Client) *CustodianStore {
	return &CustodianStore{client: client}
}

func (s *CustodianStore) SourceEventSeen(ctx context.Context, provider, eventID string) (bool, error) {
	n, err := s.client.SourceEvent.Query().
		Where(sourceevent.Provider(provider), sourceevent.ProviderEventID(eventID)).
		Limit(1).
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("store: source event lookup %s/%s: %w", provider, eventID, err)
	}
	return n > 0, nil
}

func (s *CustodianStore) RecordSourceEvent(ctx context.Context, provider string, cl custodian.Claim) error {
	payload, err := json.Marshal(cl)
	if err != nil {
		return fmt.Errorf("store: marshal claim %s: %w", cl.ProviderEventID, err)
	}
	err = s.client.SourceEvent.Create().
		SetProvider(provider).
		SetProviderEventID(cl.ProviderEventID).
		SetPayload(payload).
		Exec(ctx)
	// A concurrent delivery of the same claim is fine: one marker wins.
	if err != nil && !ent.IsConstraintError(err) {
		return fmt.Errorf("store: record source event %s/%s: %w", provider, cl.ProviderEventID, err)
	}
	return nil
}

func (s *CustodianStore) ResolveAddress(ctx context.Context, chainID, address string) (string, bool, error) {
	row, err := s.client.DepositAddress.Query().
		Where(
			depositaddress.Chain(chainID),
			depositaddress.Address(address),
			depositaddress.ModeEQ(depositaddress.ModeCustodian),
			depositaddress.Active(true),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: resolve address %s/%s: %w", chainID, address, err)
	}
	return row.Account, true, nil
}

func (s *CustodianStore) AssetConfig(ctx context.Context, chainID, asset string) (custodian.AssetConfig, bool, error) {
	row, err := s.client.AssetConfig.Query().
		Where(
			assetconfig.Chain(chainID),
			assetconfig.Asset(asset),
			assetconfig.ModeEQ(assetconfig.ModeCustodian),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return custodian.AssetConfig{}, false, nil
	}
	if err != nil {
		return custodian.AssetConfig{}, false, fmt.Errorf("store: asset config %s/%s: %w", chainID, asset, err)
	}
	cfg, err := custodianConfig(row)
	return cfg, err == nil, err
}

func (s *CustodianStore) AssetConfigs(ctx context.Context, chainID string) ([]custodian.AssetConfig, error) {
	rows, err := s.client.AssetConfig.Query().
		Where(assetconfig.Chain(chainID), assetconfig.ModeEQ(assetconfig.ModeCustodian)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: asset configs %s: %w", chainID, err)
	}
	configs := make([]custodian.AssetConfig, 0, len(rows))
	for _, r := range rows {
		cfg, err := custodianConfig(r)
		if err != nil {
			return nil, err
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

func custodianConfig(r *ent.AssetConfig) (custodian.AssetConfig, error) {
	min, ok := new(big.Int).SetString(r.MinAmount, 10)
	if !ok {
		return custodian.AssetConfig{}, fmt.Errorf("store: corrupt min amount %q for %s/%s", r.MinAmount, r.Chain, r.Asset)
	}
	return custodian.AssetConfig{
		Asset:       r.Asset,
		MinAmount:   min,
		NCredit:     uint64(r.NCredit),
		NFinalize:   uint64(r.NFinalize),
		ReorgWindow: uint64(r.ReorgWindow),
	}, nil
}

func (s *CustodianStore) OpenDeposit(ctx context.Context, p custodian.OpenParams) (bool, error) {
	err := s.client.Deposit.Create().
		SetTransferID(p.TransferID).
		SetChain(p.Chain).
		SetAsset(p.Asset).
		SetAccount(p.Account).
		SetAddress(p.Address).
		SetAmount(p.Amount.String()).
		SetMode(entdeposit.ModeCustodian).
		SetState(entdeposit.State(p.State)).
		SetBlockHeight(int64(p.Height)).
		SetBlockHash(p.Hash).
		SetTxHash(p.TxHash).
		SetSourceEvent(p.SourceEvent).
		Exec(ctx)
	if ent.IsConstraintError(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: open deposit %s: %w", p.TransferID, err)
	}
	return true, nil
}

func (s *CustodianStore) DepositState(ctx context.Context, transferID string) (deposit.State, error) {
	return depositState(ctx, s.client, transferID)
}

func (s *CustodianStore) TrackedDeposits(ctx context.Context, chainID string) ([]custodian.Tracked, error) {
	rows, err := s.client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.ModeEQ(entdeposit.ModeCustodian),
			entdeposit.StateIn(entdeposit.StatePENDING, entdeposit.StateCREDITED),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: tracked deposits %s: %w", chainID, err)
	}
	var tracked []custodian.Tracked
	for _, r := range rows {
		if r.BlockHeight == nil || r.BlockHash == nil {
			continue
		}
		tracked = append(tracked, custodian.Tracked{
			TransferID: r.TransferID,
			Asset:      r.Asset,
			State:      deposit.State(r.State),
			Height:     uint64(*r.BlockHeight),
			BlockHash:  *r.BlockHash,
		})
	}
	return tracked, nil
}

func (s *CustodianStore) ReorgedDeposits(ctx context.Context, chainID string) ([]custodian.Reorged, error) {
	rows, err := s.client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.ModeEQ(entdeposit.ModeCustodian),
			entdeposit.StateEQ(entdeposit.StateREORGED),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: reorged deposits %s: %w", chainID, err)
	}
	var reorged []custodian.Reorged
	for _, r := range rows {
		entry := custodian.Reorged{TransferID: r.TransferID, Asset: r.Asset}
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

func (s *CustodianStore) MarkReorged(ctx context.Context, transferID string, height uint64) error {
	return markReorged(ctx, s.client, transferID, height)
}

func (s *CustodianStore) ReincludeDeposit(ctx context.Context, transferID string, height uint64, hash string) error {
	return reincludeDeposit(ctx, s.client, transferID, height, hash)
}

func (s *CustodianStore) HasCredit(ctx context.Context, transferID string) (bool, error) {
	return hasCredit(ctx, s.client, transferID)
}

func (s *CustodianStore) LedgerTotal(ctx context.Context, chainID, asset string) (*big.Int, error) {
	accounts, err := s.client.DepositAddress.Query().
		Where(
			depositaddress.Chain(chainID),
			depositaddress.ModeEQ(depositaddress.ModeCustodian),
			depositaddress.Active(true),
		).
		Select(depositaddress.FieldAccount).
		Strings(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: custodian accounts %s: %w", chainID, err)
	}
	if len(accounts) == 0 {
		return new(big.Int), nil
	}
	rows, err := s.client.AccountBalance.Query().
		Where(
			accountbalance.AccountIn(accounts...),
			accountbalance.Asset(asset),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: ledger total %s/%s: %w", chainID, asset, err)
	}
	total := new(big.Int)
	for _, r := range rows {
		b, ok := new(big.Int).SetString(r.Balance, 10)
		if !ok {
			return nil, fmt.Errorf("store: corrupt balance %q for %s/%s", r.Balance, r.Account, asset)
		}
		total.Add(total, b)
	}
	return total, nil
}
