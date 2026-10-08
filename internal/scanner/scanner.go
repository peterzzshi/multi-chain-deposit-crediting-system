package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// Store is the scanner's persistence boundary; implemented by internal/store.
type Store interface {
	Cursor(ctx context.Context, chainID string) (Cursor, bool, error)
	SaveCursor(ctx context.Context, chainID string, c Cursor) error
	RecordBlock(ctx context.Context, chainID string, height uint64, hash string) error
	// BlockHashAt returns "" when the height is not recorded.
	BlockHashAt(ctx context.Context, chainID string, height uint64) (string, error)
	DropBlocksAbove(ctx context.Context, chainID string, height uint64) error
	ResolveRecipients(ctx context.Context, chainID string, addrs []string) (map[string]string, error)
	// OpenDeposit returns StateCreated if created, or the existing state if already exists.
	OpenDeposit(ctx context.Context, p domain.OpenDepositParams) (domain.State, error)
	ReincludeDeposit(ctx context.Context, transferID string, height uint64, hash string) error
	TrackedDeposits(ctx context.Context, chainID string) ([]domain.TrackedDeposit, error)
	ReorgedDeposits(ctx context.Context, chainID string) ([]domain.ReorgedDeposit, error)
	MarkReorged(ctx context.Context, transferID string, height uint64) error
	HasCredit(ctx context.Context, transferID string) (bool, error)
}

type Scanner struct {
	chain        adapters.Client
	store        Store
	engine       *credit.Engine
	cfg          Config
	assetConfigs map[string]domain.AssetPolicy // asset -> config, pre-filtered for this chain + self_built mode
}

func New(cfg Config, c adapters.Client, st Store, e *credit.Engine, assetConfigs map[string]domain.AssetPolicy) *Scanner {
	return &Scanner{
		chain:        c,
		store:        st,
		engine:       e,
		cfg:          cfg,
		assetConfigs: assetConfigs,
	}
}

// Run polls until ctx is done. errs.ErrChainInconsistent stops the scanner.
func (s *Scanner) Run(ctx context.Context) error {
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
	head, err := s.chain.Head(ctx)
	if err != nil {
		return fmt.Errorf("scanner: head: %w", err)
	}
	loaded, err := s.loadCursor(ctx)
	if err != nil {
		return err
	}
	cursor, err := s.rewindIfReorged(ctx, loaded, head)
	if err != nil {
		return err
	}
	next, err := s.scanBlocks(ctx, cursor, head)
	if err != nil {
		return err
	}
	if next == loaded {
		// Nothing scanned or rewound, so no deposit can have crossed a
		// depth threshold. Cursor, not head: a same-height reorg moves
		// the cursor but not head.
		return nil
	}
	if err := s.advanceDepth(ctx, head); err != nil {
		return err
	}
	return s.expireReorged(ctx, head)
}

func (s *Scanner) loadCursor(ctx context.Context) (Cursor, error) {
	cursor, found, err := s.store.Cursor(ctx, string(s.cfg.ChainID))
	if err != nil {
		return Cursor{}, fmt.Errorf("scanner: cursor: %w", err)
	}
	if !found {
		return Cursor{Height: max(s.cfg.StartHeight, 1) - 1}, nil
	}
	return cursor, nil
}

// scanBlocks scans up to MaxBatch blocks, carrying the cursor forward.
func (s *Scanner) scanBlocks(ctx context.Context, cursor Cursor, head uint64) (Cursor, error) {
	limit := min(head, cursor.Height+s.cfg.MaxBatch)
	for h := cursor.Height + 1; h <= limit; h++ {
		next, err := s.processBlock(ctx, h, cursor)
		if err != nil {
			return cursor, err
		}
		cursor = next
	}
	return cursor, nil
}

// rewindIfReorged marks deposits orphaned by a reorg and returns the reset cursor.
func (s *Scanner) rewindIfReorged(ctx context.Context, cursor Cursor, head uint64) (Cursor, error) {
	if cursor.Hash == "" {
		return cursor, nil
	}
	canonical, found, err := s.chain.BlockHash(ctx, cursor.Height)
	if err != nil {
		return cursor, fmt.Errorf("scanner: canonical hash at %d: %w", cursor.Height, err)
	}
	if found && canonical == cursor.Hash {
		return cursor, nil
	}
	fork, forkHash, err := s.findFork(ctx, cursor.Height)
	if err != nil {
		return cursor, err
	}
	tracked, err := s.store.TrackedDeposits(ctx, string(s.cfg.ChainID))
	if err != nil {
		return cursor, fmt.Errorf("scanner: tracked deposits: %w", err)
	}
	for _, t := range tracked {
		if t.Height <= fork || t.Height > cursor.Height {
			continue
		}
		if err := s.engine.ApplyReorg(ctx, t.TransferID, head); err != nil {
			return cursor, fmt.Errorf("scanner: reorg out %s: %w", t.TransferID, err)
		}
	}
	if err := s.store.DropBlocksAbove(ctx, string(s.cfg.ChainID), fork); err != nil {
		return cursor, fmt.Errorf("scanner: drop blocks above %d: %w", fork, err)
	}
	next := Cursor{Height: fork, Hash: forkHash}
	if err := s.store.SaveCursor(ctx, string(s.cfg.ChainID), next); err != nil {
		return cursor, fmt.Errorf("scanner: save cursor %d: %w", fork, err)
	}
	return next, nil
}

// findFork returns the deepest height below from whose recorded hash still matches
// the canonical chain.
func (s *Scanner) findFork(ctx context.Context, from uint64) (uint64, string, error) {
	if from == 0 {
		return 0, "", nil
	}
	chainID := string(s.cfg.ChainID)
	for height := from - 1; height > 0; height-- {
		stored, err := s.store.BlockHashAt(ctx, chainID, height)
		if err != nil {
			return 0, "", fmt.Errorf("scanner: recorded hash at %d: %w", height, err)
		}
		canonical, found, err := s.chain.BlockHash(ctx, height)
		if err != nil {
			return 0, "", fmt.Errorf("scanner: canonical hash at %d: %w", height, err)
		}
		if !found {
			continue // beyond canonical head: still orphaned
		}
		if stored == "" || stored == canonical {
			return height, canonical, nil
		}
	}
	return 0, "", nil
}

func (s *Scanner) processBlock(ctx context.Context, height uint64, cursor Cursor) (Cursor, error) {
	b, err := s.chain.Block(ctx, height)
	if err != nil {
		return cursor, fmt.Errorf("scanner: block %d: %w", height, err)
	}
	if cursor.Hash != "" && b.ParentHash != cursor.Hash {
		return cursor, fmt.Errorf("%w: block %d parent %s does not extend cursor %s", errs.ErrChainInconsistent, height, b.ParentHash, cursor.Hash)
	}
	recipients, err := s.store.ResolveRecipients(ctx, string(s.cfg.ChainID), transferTargets(b.Transfers))
	if err != nil {
		return cursor, fmt.Errorf("scanner: resolve recipients at %d: %w", height, err)
	}
	if len(recipients) > 0 {
		for _, tr := range b.Transfers {
			account, ok := recipients[tr.To]
			if !ok {
				continue
			}
			cfg, ok := s.assetConfigs[tr.Asset]
			if !ok {
				continue // unsupported or custodian-mode asset
			}
			if err := s.openTransfer(ctx, cfg, tr, account, b); err != nil {
				return cursor, err
			}
		}
	}
	if err := s.store.RecordBlock(ctx, string(s.cfg.ChainID), b.Height, b.Hash); err != nil {
		return cursor, fmt.Errorf("scanner: record block %d: %w", height, err)
	}
	next := Cursor{Height: b.Height, Hash: b.Hash}
	if err := s.store.SaveCursor(ctx, string(s.cfg.ChainID), next); err != nil {
		return cursor, fmt.Errorf("scanner: save cursor %d: %w", height, err)
	}
	return next, nil
}

func (s *Scanner) openTransfer(ctx context.Context, cfg domain.AssetPolicy, tr adapters.Transfer, account string, b adapters.Block) error {
	id, err := tr.LogicalID(s.cfg.ChainID)
	if err != nil {
		return err
	}
	transferID := id.String()
	event := domain.EventObserved
	if tr.Amount.Cmp(cfg.MinAmount) < 0 {
		event = domain.EventObservedBelowMinimum
	}
	state, err := s.store.OpenDeposit(ctx, domain.OpenDepositParams{
		TransferID: transferID,
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
	switch state {
	case domain.StateCreated:
		return s.engine.Apply(ctx, transferID, event)
	case domain.StateReorged, domain.StateReversed:
		if err := s.store.ReincludeDeposit(ctx, transferID, b.Height, b.Hash); err != nil {
			return fmt.Errorf("scanner: reinclude %s: %w", transferID, err)
		}
		return s.engine.Apply(ctx, transferID, domain.EventReincluded)
	default:
		return nil
	}
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
	processor := credit.NewDepositDepthProcessor(s.engine, s.assetConfigs)
	for _, t := range tracked {
		if _, err := processor.ProcessTrackedDepth(ctx, t, head); err != nil {
			return fmt.Errorf("scanner: %w", err)
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
	processor := credit.NewDepositDepthProcessor(s.engine, s.assetConfigs)
	for _, r := range reorged {
		if r.Height == nil {
			// Recover rows whose reorg marker was not written.
			if err := s.store.MarkReorged(ctx, r.TransferID, head); err != nil {
				return fmt.Errorf("scanner: repair reorg mark %s: %w", r.TransferID, err)
			}
			continue
		}
		result, err := processor.ProcessReorgedExpiry(ctx, r.TransferID, r.Asset, r.Height, head, s.store.HasCredit)
		if err != nil {
			return fmt.Errorf("scanner: %w", err)
		}
		if result.ShouldApply {
			if err := s.engine.Apply(ctx, r.TransferID, result.Event); err != nil {
				return fmt.Errorf("scanner: expire %s: %w", r.TransferID, err)
			}
		}
	}
	return nil
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
