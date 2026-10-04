-- Seed data for local manual verification (docs/manual-verification.md).
-- Apply after any app binary has created the schema:
--   docker compose exec -T postgres psql -U postgres -d deposit_crediting < scripts/seed.sql
-- Asset configs come from configs/assets.json (applied by the binaries at
-- startup); this file only seeds deposit addresses. Idempotent.

INSERT INTO deposit_addresses (account, chain, address, mode, active)
VALUES
  ('alice', 'stubchain', '0xaaa', 'self_built', true),
  ('carol', 'stubchain', '0xccc', 'self_built', true),
  ('bob',   'stubchain', '0xbbb', 'custodian',  true)
ON CONFLICT (chain, address) DO NOTHING;
