package credit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"

	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
)

type View struct {
	State       domain.State
	Chain       string
	Account     string
	Asset       string
	Amount      *big.Int
	Held        bool
	CreditCycle int
}

type Balance struct {
	Amount  *big.Int
	Held    *big.Int
	Version int
}

type depositRepo interface {
	DepositForUpdate(ctx context.Context, transferID string) (View, error)
	SetDepositState(ctx context.Context, transferID string, state domain.State) error
	SetCreditCycle(ctx context.Context, transferID string, cycle int) error
	SetReorgedHeight(ctx context.Context, transferID string, height uint64) error
}

type ledgerRepo interface {
	InsertEntry(ctx context.Context, e Entry) error
	HasEntry(ctx context.Context, ref string) (bool, error)
	ReversalOf(ctx context.Context, ref string) (bool, error)
	BalanceForUpdate(ctx context.Context, account, asset string) (Balance, error)
	SetBalance(ctx context.Context, account, asset string, balance *big.Int) error
	SetBalanceOptimistic(ctx context.Context, account, asset string, balance *big.Int, expectedVersion int) (bool, error)
}

type holdRepo interface {
	HoldsActive(ctx context.Context, chain, asset string) (active bool, tier *big.Int, err error)
	SetHeld(ctx context.Context, transferID, account, asset string, amount *big.Int) error
	ClearHeld(ctx context.Context, transferID, account, asset string, amount *big.Int) error
}

// Tx is what the engine orchestrates over; helpers take only the capability
// they use.
type Tx interface {
	depositRepo
	ledgerRepo
	holdRepo
}

type Store interface {
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

type Engine struct {
	store Store
}

func NewEngine(s Store) *Engine {
	return &Engine{store: s}
}

func shouldHold(active bool, tier *big.Int, amount *big.Int) bool {
	return active && (tier == nil || amount.Cmp(tier) >= 0)
}

type posting struct {
	entry  Entry
	before *big.Int
	after  *big.Int
}

// txRecord is filled inside the closure and emitted only after commit: a log
// written inside can describe a balance move the commit then discards.
type txRecord struct {
	deposit  View
	next     domain.State
	event    domain.Event
	effect   domain.Effect
	postings []posting
}

func (r txRecord) emit(ctx context.Context, transferID string) {
	for _, p := range r.postings {
		slog.InfoContext(ctx, "ledger entry posted",
			"account", p.entry.Account,
			"asset", p.entry.Asset,
			"type", p.entry.Type,
			"amount", p.entry.Amount.String(),
			"ref", p.entry.Ref,
			"balance_before", p.before.String(),
			"balance_after", p.after.String())
	}
	switch r.effect {
	case domain.EffectCredit:
		slog.InfoContext(ctx, "credited deposit",
			"transfer_id", transferID,
			"account", r.deposit.Account,
			"asset", r.deposit.Asset,
			"amount", r.deposit.Amount.String(),
			"chain", r.deposit.Chain,
			"cycle", r.deposit.CreditCycle)
	case domain.EffectReverse:
		slog.WarnContext(ctx, "reversed deposit",
			"transfer_id", transferID,
			"account", r.deposit.Account,
			"asset", r.deposit.Asset,
			"amount", r.deposit.Amount.String(),
			"chain", r.deposit.Chain,
			"held_cleared", r.deposit.Held)
	}
	slog.InfoContext(ctx, "deposit state transition",
		"transfer_id", transferID,
		"from", r.deposit.State,
		"to", r.next,
		"event", r.event)
}

func (e *Engine) Apply(ctx context.Context, transferID string, ev domain.Event) error {
	var rec txRecord
	err := e.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		dep, err := tx.DepositForUpdate(ctx, transferID)
		if err != nil {
			return fmt.Errorf("credit: load deposit: %w", err)
		}
		next, effect, err := domain.Transition(dep.State, ev)
		if err != nil {
			return fmt.Errorf("credit: apply %s to %s: %w", ev, transferID, err)
		}
		rec = txRecord{deposit: dep, next: next, event: ev, effect: effect}
		switch effect {
		case domain.EffectCredit:
			postings, err := creditFunds(ctx, tx, dep, transferID)
			if err != nil {
				return err
			}
			rec.postings = postings
		case domain.EffectReverse:
			original, err := New(Credit, dep.Account, dep.Asset, dep.Amount, creditRef(transferID, dep.CreditCycle))
			if err != nil {
				return fmt.Errorf("credit: rebuild credit: %w", err)
			}
			reversal, err := Reverse(original)
			if err != nil {
				return fmt.Errorf("credit: build reversal: %w", err)
			}
			p, err := post(ctx, tx, reversal)
			if err != nil {
				return err
			}
			rec.postings = append(rec.postings, p)
			if dep.Held {
				if err := tx.ClearHeld(ctx, transferID, dep.Account, dep.Asset, dep.Amount); err != nil {
					return err
				}
			}
		}
		if err := tx.SetDepositState(ctx, transferID, next); err != nil {
			return fmt.Errorf("credit: set state %s: %w", next, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	rec.emit(ctx, transferID)
	return nil
}

// ApplyReorg atomically marks a deposit REORGED and starts its reorg window.
func (e *Engine) ApplyReorg(ctx context.Context, transferID string, head uint64) error {
	var dep View
	err := e.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		d, err := tx.DepositForUpdate(ctx, transferID)
		if err != nil {
			return fmt.Errorf("credit: load deposit: %w", err)
		}
		next, _, err := domain.Transition(d.State, domain.EventReorgedOut)
		if err != nil {
			return fmt.Errorf("credit: reorg out %s: %w", transferID, err)
		}
		dep = d
		if err := tx.SetReorgedHeight(ctx, transferID, head); err != nil {
			return fmt.Errorf("credit: mark reorged %s: %w", transferID, err)
		}
		if err := tx.SetDepositState(ctx, transferID, next); err != nil {
			return fmt.Errorf("credit: set state %s: %w", next, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	slog.WarnContext(ctx, "deposit reorged out",
		"transfer_id", transferID,
		"account", dep.Account,
		"asset", dep.Asset,
		"amount", dep.Amount.String(),
		"chain", dep.Chain,
		"reorg_height", head,
		"from_state", dep.State)
	return nil
}

// creditRef namespaces re-credits by cycle; cycle 0 is the original and keeps
// the bare transfer id.
func creditRef(transferID string, cycle int) string {
	if cycle == 0 {
		return transferID
	}
	return fmt.Sprintf("recredit:%s:%d", transferID, cycle)
}

// creditFunds applies an idempotent credit, creating a new cycle after reversal.
// It returns the entries it posted, for the caller to log after commit.
func creditFunds(ctx context.Context, tx Tx, dep View, transferID string) ([]posting, error) {
	credited, err := tx.HasEntry(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("credit: check entry %s: %w", transferID, err)
	}
	if !credited {
		p, err := postNew(ctx, tx, Credit, dep, transferID)
		if err != nil {
			return nil, err
		}
		if err := holdIfCapped(ctx, tx, dep, transferID); err != nil {
			return nil, err
		}
		return []posting{p}, nil
	}
	latest := creditRef(transferID, dep.CreditCycle)
	reversed, err := tx.ReversalOf(ctx, latest)
	if err != nil {
		return nil, fmt.Errorf("credit: check reversal %s: %w", latest, err)
	}
	if !reversed {
		return nil, nil
	}
	cycle := dep.CreditCycle + 1
	p, err := postNew(ctx, tx, Credit, dep, creditRef(transferID, cycle))
	if errors.Is(err, errs.ErrDuplicateRef) {
		return nil, nil // re-credit already posted
	}
	if err != nil {
		return nil, err
	}
	if err := tx.SetCreditCycle(ctx, transferID, cycle); err != nil {
		return nil, fmt.Errorf("credit: set credit cycle: %w", err)
	}
	if err := holdIfCapped(ctx, tx, dep, transferID); err != nil {
		return nil, err
	}
	return []posting{p}, nil
}

func holdIfCapped(ctx context.Context, tx holdRepo, dep View, transferID string) error {
	active, tier, err := tx.HoldsActive(ctx, dep.Chain, dep.Asset)
	if err != nil {
		return fmt.Errorf("credit: exposure holds %s/%s: %w", dep.Chain, dep.Asset, err)
	}
	if !shouldHold(active, tier, dep.Amount) {
		return nil
	}
	return tx.SetHeld(ctx, transferID, dep.Account, dep.Asset, dep.Amount)
}

// Debit posts an external debit (withdrawal, trade) against spendable funds
// (balance - held); ref is the idempotency key. Hot platform accounts use
// optimistic locking with retry under contention, user accounts pessimistic.
func (e *Engine) Debit(ctx context.Context, account, asset string, amount *big.Int, ref string) error {
	entry, err := New(Debit, account, asset, amount, ref)
	if err != nil {
		return fmt.Errorf("credit: build debit: %w", err)
	}

	switch account {
	case "hot_wallet_stubchain", "hot_wallet_fastchain", "fee_collection", "liquidity_pool":
		const maxRetries = 5
		for attempt := range maxRetries {
			err := e.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
				return debit(ctx, tx, account, asset, entry, writeOptimistic)
			})
			if err == nil {
				slog.InfoContext(ctx, "debited hot account",
					"account", account,
					"asset", asset,
					"amount", amount.String(),
					"ref", ref,
					"attempt", attempt+1)
				return nil
			}
			if !errors.Is(err, errs.ErrVersionConflict) {
				return err
			}
			slog.InfoContext(ctx, "optimistic retry",
				"account", account,
				"asset", asset,
				"attempt", attempt+1)
		}
		return fmt.Errorf("credit: debit %s failed after %d retries", ref, maxRetries)

	default:
		err := e.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
			return debit(ctx, tx, account, asset, entry, writeIsolated)
		})
		if err == nil {
			slog.InfoContext(ctx, "debited user account",
				"account", account,
				"asset", asset,
				"amount", amount.String(),
				"ref", ref)
		}
		return err
	}
}

type balanceWriter func(ctx context.Context, tx ledgerRepo, account, asset string, balance *big.Int, version int) error

func writeOptimistic(ctx context.Context, tx ledgerRepo, account, asset string, balance *big.Int, version int) error {
	committed, err := tx.SetBalanceOptimistic(ctx, account, asset, balance, version)
	if err != nil {
		return err
	}
	if !committed {
		return errs.ErrVersionConflict
	}
	return nil
}

func writeIsolated(ctx context.Context, tx ledgerRepo, account, asset string, balance *big.Int, _ int) error {
	return tx.SetBalance(ctx, account, asset, balance)
}

// debit posts one debit entry and commits the new balance via the given write
// strategy. A duplicate ref is a no-op idempotent retry: the entry already
// exists, so the balance was written by whichever attempt inserted it.
func debit(ctx context.Context, tx ledgerRepo, account, asset string, entry Entry, write balanceWriter) error {
	bal, err := tx.BalanceForUpdate(ctx, account, asset)
	if err != nil {
		return fmt.Errorf("credit: load balance: %w", err)
	}
	if err := tx.InsertEntry(ctx, entry); errors.Is(err, errs.ErrDuplicateRef) {
		return nil
	} else if err != nil {
		return fmt.Errorf("credit: insert debit %s: %w", entry.Ref, err)
	}
	available := new(big.Int).Sub(bal.Amount, bal.Held)
	if _, err := Apply(available, entry); err != nil {
		return err
	}
	next, err := Apply(bal.Amount, entry)
	if err != nil {
		return err
	}
	if err := write(ctx, tx, account, asset, next, bal.Version); err != nil {
		return fmt.Errorf("credit: set balance: %w", err)
	}
	return nil
}

func postNew(ctx context.Context, tx ledgerRepo, typ TransactionType, dep View, ref string) (posting, error) {
	entry, err := New(typ, dep.Account, dep.Asset, dep.Amount, ref)
	if err != nil {
		return posting{}, fmt.Errorf("credit: build entry: %w", err)
	}
	return post(ctx, tx, entry)
}

// post appends an entry and updates the balance projection; the caller logs the
// returned posting once the transaction commits.
func post(ctx context.Context, tx ledgerRepo, entry Entry) (posting, error) {
	if err := tx.InsertEntry(ctx, entry); err != nil {
		return posting{}, fmt.Errorf("credit: insert %s %s: %w", entry.Type, entry.Ref, err)
	}
	bal, err := tx.BalanceForUpdate(ctx, entry.Account, entry.Asset)
	if err != nil {
		return posting{}, fmt.Errorf("credit: load balance: %w", err)
	}
	next, err := Apply(bal.Amount, entry)
	if err != nil {
		return posting{}, err
	}
	if err := tx.SetBalance(ctx, entry.Account, entry.Asset, next); err != nil {
		return posting{}, fmt.Errorf("credit: set balance: %w", err)
	}
	return posting{entry: entry, before: bal.Amount, after: next}, nil
}
