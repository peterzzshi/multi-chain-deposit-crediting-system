-- Seed data for local manual verification (docs/manual-verification.md).
-- Apply after any app binary has created the schema:
--   docker compose exec -T postgres psql -U postgres -d deposit_crediting < scripts/seed.sql
-- Idempotent: re-running upserts configs and ignores duplicate addresses.

INSERT INTO asset_configs (chain, asset, decimals, mode, min_amount, n_credit, n_finalize, reorg_window, exposure_cap, tier_amount)
VALUES
  -- Self-built ETH: credit after 2 confirmations, finalize after 4,
  -- 3-block reorg window, exposure cap with a high-value tier.
  ('stubchain', 'ETH', 18, 'self_built', '100', 2, 4, 3, '5000', '2000'),
  -- Custodian USDT: same depths, no exposure cap (NULL = uncapped).
  ('stubchain', 'USDT', 6, 'custodian', '50', 2, 4, 3, NULL, NULL)
ON CONFLICT (chain, asset) DO UPDATE SET
  decimals = EXCLUDED.decimals,
  mode = EXCLUDED.mode,
  min_amount = EXCLUDED.min_amount,
  n_credit = EXCLUDED.n_credit,
  n_finalize = EXCLUDED.n_finalize,
  reorg_window = EXCLUDED.reorg_window,
  exposure_cap = EXCLUDED.exposure_cap,
  tier_amount = EXCLUDED.tier_amount;

INSERT INTO deposit_addresses (account, chain, address, mode, active)
VALUES
  ('alice', 'stubchain', '0xaaa', 'self_built', true),
  ('carol', 'stubchain', '0xccc', 'self_built', true),
  ('bob',   'stubchain', '0xbbb', 'custodian',  true)
ON CONFLICT (chain, address) DO NOTHING;
