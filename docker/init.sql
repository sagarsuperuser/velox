-- Create a non-superuser app role for the application.
-- Superusers bypass RLS, so the app must connect as a non-superuser.
CREATE ROLE velox_app WITH LOGIN PASSWORD 'velox_app';
GRANT ALL PRIVILEGES ON DATABASE velox TO velox_app;
GRANT ALL ON SCHEMA public TO velox_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO velox_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO velox_app;
