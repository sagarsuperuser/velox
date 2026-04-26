-- Velox single-VM Postgres init.
--
-- Velox connects via two roles:
--   velox       — owner / migration role (the POSTGRES_USER above)
--   velox_app   — least-privilege runtime role; this is where RLS is enforced.
--
-- Why a separate app role: superusers and database owners bypass RLS. The
-- binary derives the app URL by swapping credentials with velox_app/velox_app
-- (see cmd/velox/main.go:deriveAppURL). If the role doesn't exist the binary
-- falls back to the admin pool with a loud warning — RLS NOT enforced — so
-- creating this role is a hard requirement for safe self-host.

CREATE ROLE velox_app WITH LOGIN PASSWORD 'velox_app';
GRANT ALL PRIVILEGES ON DATABASE velox TO velox_app;
GRANT ALL ON SCHEMA public TO velox_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO velox_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO velox_app;
