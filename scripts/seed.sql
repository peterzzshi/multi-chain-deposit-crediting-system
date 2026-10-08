-- Seed data for local manual verification (docs/manual-verification.md).
-- Apply after any app binary has created the schema:
--   docker compose exec -T postgres psql -U postgres -d deposit_crediting < scripts/seed.sql
-- Asset configs come from configs/assets.json (applied by the binaries at
-- startup); this file only seeds deposit addresses. Idempotent.

INSERT INTO deposit_addresses (account, chain, address, active)
VALUES
  -- stubchain addresses (scenarios 1-20, 25-29)
  ('alice', 'stubchain', '0xaaa', true),
  ('carol', 'stubchain', '0xccc', true),
  ('bob',   'stubchain', '0xbbb', true),
  ('dave',  'stubchain', '0xddd', true),
  -- fastchain addresses (scenario 21)
  ('eve',   'fastchain', '0xeee', true)
ON CONFLICT (account, chain) DO NOTHING;
