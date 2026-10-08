package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"deposit-crediting/internal/custodian"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/ent/accountbalance"
	"deposit-crediting/internal/store/ent/depositaddress"
	"deposit-crediting/internal/store/ent/sourceevent"
)

// CustodianStore implements custodian.Store over the ent client.
type CustodianStore struct {
	client *ent.Client
	*DepositOps
}

func NewCustodianStore(client *ent.Client) *CustodianStore {
	return &CustodianStore{
		client:     client,
		DepositOps: NewDepositOps(client),
	}
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

func (s *CustodianStore) OpenDeposit(ctx context.Context, p domain.OpenDepositParams) (domain.State, error) {
	create := s.client.Deposit.Create().
		SetTransferID(p.TransferID).
		SetChain(p.Chain).
		SetAsset(p.Asset).
		SetAccount(p.Account).
		SetAddress(p.Address).
		SetAmount(p.Amount.String()).
		SetMode(domain.ModeCustodian).
		SetState(domain.StateCreated).
		SetBlockHeight(int64(p.Height)).
		SetBlockHash(p.Hash).
		SetTxHash(p.TxHash).
		SetSourceEvent(p.SourceEvent)

	return execOpenDeposit(ctx, create, p.TransferID, s.client)
}

func (s *CustodianStore) TrackedDeposits(ctx context.Context, chainID string) ([]domain.TrackedDeposit, error) {
	rows, err := queryTrackedDeposits(s.client, chainID, domain.ModeCustodian).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: tracked deposits %s: %w", chainID, err)
	}
	var tracked []domain.TrackedDeposit
	for _, r := range rows {
		if r.BlockHeight == nil || r.BlockHash == nil {
			continue
		}
		tracked = append(tracked, domain.TrackedDeposit{
			TransferID: r.TransferID,
			Asset:      r.Asset,
			State:      r.State,
			Height:     uint64(*r.BlockHeight),
			BlockHash:  *r.BlockHash,
		})
	}
	return tracked, nil
}

func (s *CustodianStore) ReorgedDeposits(ctx context.Context, chainID string) ([]domain.ReorgedDeposit, error) {
	rows, err := queryReorgedDeposits(s.client, chainID, domain.ModeCustodian, domain.StateReorged, domain.StateReversed).All(ctx)
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

func (s *CustodianStore) LedgerTotal(ctx context.Context, chainID, asset string) (*big.Int, error) {
	accounts, err := s.client.DepositAddress.Query().
		Where(
			depositaddress.Chain(chainID),
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
