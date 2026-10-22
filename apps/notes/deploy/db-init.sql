-- Databases of the notes app, created if missing by the notes-db-init job
-- (apps/notes/compose.yaml). Runs as the Postgres superuser on every `make up`,
-- so every statement must be idempotent. Each database is owned by its own role:
-- not a superuser, can't bypass row-level security; tables come from migrations.
SELECT 'CREATE ROLE note LOGIN PASSWORD ''note'' NOSUPERUSER NOBYPASSRLS'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'note')\gexec

SELECT 'CREATE DATABASE note OWNER note'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'note')\gexec
