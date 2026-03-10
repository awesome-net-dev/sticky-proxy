-- Seed script for sticky-proxy postgres assignment load tests.
-- Creates the accounts table and inserts 200 test users.
--
-- Usage:
--   docker compose -f docker-compose.postgres.yml exec postgres \
--     psql -U stickyproxy -d stickyproxy -f /seed.sql
--
-- Or pipe directly:
--   cat k6/seed_postgres.sql | docker compose -f docker-compose.postgres.yml exec -T postgres \
--     psql -U stickyproxy -d stickyproxy

CREATE TABLE IF NOT EXISTS accounts (
    id TEXT PRIMARY KEY,
    weight INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO accounts (id, weight)
SELECT i::text, 1
FROM generate_series(0, 199) AS i
ON CONFLICT (id) DO NOTHING;
