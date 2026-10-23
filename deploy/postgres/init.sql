-- Platform database: the admin dashboard's accounts. Idempotent: run by
-- Postgres on an empty data volume and by the platform-db-init job on every
-- `make up`. Apps create their own databases (apps/<app>/deploy/db-init.sql).
-- The role is not a superuser and can't bypass row-level security; the service
-- creates its tables through migrations.
SELECT 'CREATE ROLE admin LOGIN PASSWORD ''admin'' NOSUPERUSER NOBYPASSRLS'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'admin')\gexec

SELECT 'CREATE DATABASE admin OWNER admin'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'admin')\gexec
