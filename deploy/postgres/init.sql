-- Platform databases. Runs once, on an empty data volume. One database per
-- service, each owned by its own role. The roles are not superusers and can't
-- bypass row-level security; the services create their tables through migrations.
-- Apps create their own databases (apps/<app>/deploy/db-init.sql).
CREATE ROLE iam  LOGIN PASSWORD 'iam'  NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE iam OWNER iam;

-- centralized authorization (ReBAC); schema: deploy/postgres/authz/init.sql
CREATE ROLE authz LOGIN PASSWORD 'authz' NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE authz OWNER authz;
