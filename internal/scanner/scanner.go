package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
)

type Config struct {
	ChainID      domain.NetworkID
	StartHeight  uint64
	MaxBatch     uint64
	PollInterval time.Duration
}

type Cursor struct {
	Height uint64
	Hash   string
}

type AssetConfig struct {
	Asset       string
	MinAmount   *big.Int
	NCredit     uint64
	NFinalize   uint64
	ReorgWindow uint64
}

type Tracked struct {
	TransferID string
	Asset      string
	State      domain.State
	Height     uint64
}

type Reorged struct {
	TransferID string
	Asset      string
	Height     *uint64
}

type OpenParams struct {
	TransferID string
	State      domain.State
	Chain      string
	Asset      string
	Account    string
	Address    string
	Amount     *big.Int
	Height     uint64
	Hash       string
	TxHash     string
}

// Store is the scanner's persistence boundary; implemented by
// internal/store. Queries are scoped to one chain; the asset policy decides
// whether a matched transfer uses the self-built path.
type Store interface {
	Cursor(ctx context.Context, chainID string) (Cursor, bool, error)
	SaveCursor(ctx context.Context, chainID string, c Cursor) error
	RecordBlock(ctx context.Context, chainID string, height uint64, hash string) error
	// BlockHashAt returns "" when the height is not recorded.
	BlockHashAt(ctx context.Context, chainID string, height uint64) (string, error)
	DropBlocksAbove(ctx context.Context, chainID string, height uint64) error
	AssetConfigs(ctx context.Context, chainID string) ([]AssetConfig, error)
	ResolveRecipients(ctx context.Context, chainID string, addrs []string) (map[string]string, error)
	OpenDeposit(ctx context.Context, p OpenParams) (domain.OpenResult, error)
	ReincludeDeposit(ctx context.Context, transferID string, height uint64, hash string) error
	DepositState(ctx context.Context, transferID string) (domain.State, error)
	TrackedDeposits(ctx context.Context, chainID string) ([]Tracked, error)
	ReorgedDeposits(ctx context.Context, chainID string) ([]Reorged, error)
	MarkReorged(ctx context.Context, transferID string, height uint64) error
	HasCredit(ctx context.Context, transferID string) (bool, error)
}

type Scanner struct {
	chain  adapters.Client
	store  Store
	engine *credit.Engine
	cfg    Config
}

func New(cfg Config, c adapters.Client, st Store, e *credit.Engine) *Scanner {
	return &Scanner{chain: c, store: st, engine: e, cfg: cfg}
}

// Run polls until ctx is done. Inconsistent chain data stops the scanner.
func (s *Scanner) Run(ctx context.Context) error {
	if s.cfg.PollInterval <= 0 {
		return fmt.Errorf("scanner: poll interval must be positive")
	}
	if s.cfg.MaxBatch == 0 {
		return fmt.Errorf("scanner: max batch must be positive")
	}
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := s.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			if errors.Is(err, errs.ErrChainInconsistent) {
				return err
			}
			slog.Error("scanner tick failed", "chain", s.cfg.ChainID, "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Tick runs one scan iteration.
func (s *Scanner) Tick(ctx context.Context) error {
	if s.cfg.MaxBatch == 0 {
		return fmt.Errorf("scanner: max batch must be positive")
	}
	head, err := s.chain.Head(ctx)
	if err != nil {
		return fmt.Errorf("scanner: head: %w", err)
	}
	cursor, found, err := s.store.Cursor(ctx, string(s.cfg.ChainID))
	if err != nil {
		return fmt.Errorf("scanner: cursor: %w", err)
	}
	if !found {
		start := max(s.cfg.StartHeight, 1)
		cursor = Cursor{Height: start - 1}
	}
	if err := s.rewindIfReorged(ctx, &cursor, head); err != nil {
		return err
	}
	limit := min(head, cursor.Height+s.cfg.MaxBatch)
	for h := cursor.Height + 1; h <= limit; h++ {
		if err := s.processBlock(ctx, h, &cursor); err != nil {
			return err
		}
	}
	if err := s.advanceDepth(ctx, head); err != nil {
		return err
	}
	return s.expireReorged(ctx, head)
}

// rewindIfReorged finds the fork, marks orphaned deposits, and resets the cursor.
func (s *Scanner) rewindIfReorged(ctx context.Context, cursor *Cursor, head uint64) error {
	if cursor.Hash == "" {
		return nil
	}
	canonical, found, err := s.chain.BlockHash(ctx, cursor.Height)
	if err != nil {
		return fmt.Errorf("scanner: canonical hash at %d: %w", cursor.Height, err)
	}
	if found && canonical == cursor.Hash {
		return nil
	}
	fork := cursor.Height
	var forkHash string
	for fork > 0 {
		fork--
		stored, err := s.store.BlockHashAt(ctx, string(s.cfg.ChainID), fork)
		if err != nil {
			return fmt.Errorf("scanner: recorded hash at %d: %w", fork, err)
		}
		canonical, found, err := s.chain.BlockHash(ctx, fork)
		if err != nil {
			return fmt.Errorf("scanner: canonical hash at %d: %w", fork, err)
		}
		if !found {
			continue // beyond canonical head: still orphaned
		}
		if stored == "" || stored == canonical {
			forkHash = canonical
			break
		}
	}
	tracked, err := s.store.TrackedDeposits(ctx, string(s.cfg.ChainID))
	if err != nil {
		return fmt.Errorf("scanner: tracked deposits: %w", err)
	}
	for _, t := range tracked {
		if t.Height <= fork || t.Height > cursor.Height {
			continue
		}
		if err := s.engine.ApplyReorg(ctx, t.TransferID, head); err != nil {
			return fmt.Errorf("scanner: reorg out %s: %w", t.TransferID, err)
		}
	}
	if err := s.store.DropBlocksAbove(ctx, string(s.cfg.ChainID), fork); err != nil {
		return fmt.Errorf("scanner: drop blocks above %d: %w", fork, err)
	}
	*cursor = Cursor{Height: fork, Hash: forkHash}
	return s.store.SaveCursor(ctx, string(s.cfg.ChainID), *cursor)
}

func (s *Scanner) processBlock(ctx context.Context, height uint64, cursor *Cursor) error {
	b, err := s.chain.Block(ctx, height)
	if err != nil {
		return fmt.Errorf("scanner: block %d: %w", height, err)
	}
	if cursor.Hash != "" && b.ParentHash != cursor.Hash {
		return fmt.Errorf("%w: block %d parent %s does not extend cursor %s", errs.ErrChainInconsistent, height, b.ParentHash, cursor.Hash)
	}
	recipients, err := s.store.ResolveRecipients(ctx, string(s.cfg.ChainID), transferTargets(b.Transfers))
	if err != nil {
		return fmt.Errorf("scanner: resolve recipients at %d: %w", height, err)
	}
	if len(recipients) > 0 {
		configs, err := s.configs(ctx)
		if err != nil {
			return err
		}
		for _, tr := range b.Transfers {
			account, ok := recipients[tr.To]
			if !ok {
				continue
			}
			cfg, ok := configs[tr.Asset]
			if !ok {
				continue // unsupported or custodian-mode asset
			}
			if err := s.openTransfer(ctx, cfg, tr, account, b); err != nil {
				return err
			}
		}
	}
	if err := s.store.RecordBlock(ctx, string(s.cfg.ChainID), b.Height, b.Hash); err != nil {
		return fmt.Errorf("scanner: record block %d: %w", height, err)
	}
	*cursor = Cursor{Height: b.Height, Hash: b.Hash}
	if err := s.store.SaveCursor(ctx, string(s.cfg.ChainID), *cursor); err != nil {
		return fmt.Errorf("scanner: save cursor %d: %w", height, err)
	}
	return nil
}

func (s *Scanner) openTransfer(ctx context.Context, cfg AssetConfig, tr adapters.Transfer, account string, b adapters.Block) error {
	id, err := tr.LogicalID(s.cfg.ChainID)
	if err != nil {
		return err
	}
	transferID := id.String()
	event := domain.EventObserved
	if tr.Amount.Cmp(cfg.MinAmount) < 0 {
		event = domain.EventObservedBelowMinimum
	}
	state, _, err := domain.Transition(domain.StateNone, event)
	if err != nil {
		return fmt.Errorf("scanner: initial state: %w", err)
	}
	result, err := s.store.OpenDeposit(ctx, OpenParams{
		TransferID: transferID,
		State:      state,
		Chain:      string(s.cfg.ChainID),
		Asset:      tr.Asset,
		Account:    account,
		Address:    tr.To,
		Amount:     tr.Amount,
		Height:     b.Height,
		Hash:       b.Hash,
		TxHash:     tr.TxHash,
	})
	if err != nil {
		return fmt.Errorf("scanner: open deposit %s: %w", transferID, err)
	}
	if result.Created() {
		return nil
	}
	existing := result.ExistingState()
	if existing != domain.StateReorged && existing != domain.StateReversed {
		return nil
	}
	if err := s.store.ReincludeDeposit(ctx, transferID, b.Height, b.Hash); err != nil {
		return fmt.Errorf("scanner: reinclude %s: %w", transferID, err)
	}
	if err := s.engine.Apply(ctx, transferID, domain.EventReincluded); err != nil {
		return fmt.Errorf("scanner: reinclude %s: %w", transferID, err)
	}
	return nil
}

// advanceDepth applies confirmation and finality thresholds.
func (s *Scanner) advanceDepth(ctx context.Context, head uint64) error {
	tracked, err := s.store.TrackedDeposits(ctx, string(s.cfg.ChainID))
	if err != nil {
		return fmt.Errorf("scanner: tracked deposits: %w", err)
	}
	if len(tracked) == 0 {
		return nil
	}
	configs, err := s.configs(ctx)
	if err != nil {
		return err
	}
	for _, t := range tracked {
		cfg, ok := configs[t.Asset]
		if !ok || t.Height > head {
			continue
		}
		depth := head - t.Height + 1
		switch {
		case t.State == domain.StatePending && depth >= cfg.NCredit:
			if err := s.engine.Apply(ctx, t.TransferID, domain.EventDepthReached); err != nil {
				return fmt.Errorf("scanner: credit %s: %w", t.TransferID, err)
			}
		case t.State == domain.StateCredited && depth >= cfg.NFinalize:
			if err := s.engine.Apply(ctx, t.TransferID, domain.EventFinalityReached); err != nil {
				return fmt.Errorf("scanner: finalize %s: %w", t.TransferID, err)
			}
		}
	}
	return nil
}

// expireReorged resolves deposits that remain out of the canonical chain.
func (s *Scanner) expireReorged(ctx context.Context, head uint64) error {
	reorged, err := s.store.ReorgedDeposits(ctx, string(s.cfg.ChainID))
	if err != nil {
		return fmt.Errorf("scanner: reorged deposits: %w", err)
	}
	if len(reorged) == 0 {
		return nil
	}
	configs, err := s.configs(ctx)
	if err != nil {
		return err
	}
	for _, r := range reorged {
		if r.Height == nil {
			// Recover rows whose reorg marker was not written.
			if err := s.store.MarkReorged(ctx, r.TransferID, head); err != nil {
				return fmt.Errorf("scanner: repair reorg mark %s: %w", r.TransferID, err)
			}
			continue
		}
		cfg, ok := configs[r.Asset]
		if !ok || head-*r.Height < cfg.ReorgWindow {
			continue
		}
		credited, err := s.store.HasCredit(ctx, r.TransferID)
		if err != nil {
			return fmt.Errorf("scanner: credit check %s: %w", r.TransferID, err)
		}
		event := domain.EventWindowExpiredUncredited
		if credited {
			event = domain.EventWindowExpiredCredited
		}
		if err := s.engine.Apply(ctx, r.TransferID, event); err != nil {
			return fmt.Errorf("scanner: expire %s: %w", r.TransferID, err)
		}
	}
	return nil
}

func (s *Scanner) configs(ctx context.Context) (map[string]AssetConfig, error) {
	list, err := s.store.AssetConfigs(ctx, string(s.cfg.ChainID))
	if err != nil {
		return nil, fmt.Errorf("scanner: asset configs: %w", err)
	}
	configs := make(map[string]AssetConfig, len(list))
	for _, c := range list {
		configs[c.Asset] = c
	}
	return configs, nil
}

func transferTargets(transfers []adapters.Transfer) []string {
	seen := make(map[string]struct{}, len(transfers))
	var addrs []string
	for _, tr := range transfers {
		if tr.To == "" || tr.Amount == nil {
			continue
		}
		if _, ok := seen[tr.To]; !ok {
			seen[tr.To] = struct{}{}
			addrs = append(addrs, tr.To)
		}
	}
	return addrs
}
