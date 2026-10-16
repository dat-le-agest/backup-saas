-- Runs once, on an empty data volume. One database per service, each owned by
-- its own role. The roles are not superusers and can't bypass row-level
-- security; the services create their tables through migrations.
CREATE ROLE iam  LOGIN PASSWORD 'iam'  NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE iam OWNER iam;

CREATE ROLE note LOGIN PASSWORD 'note' NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE note OWNER note;
