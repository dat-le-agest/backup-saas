#!/usr/bin/env bash
# Copy relationships of existing data into the central authz service:
#   iam.memberships (active) → tenant:<tenant>#<role>@user:<user>
#   note.notes               → note:<id>#tenant@tenant:<tenant>, note:<id>#owner@user:<created_by>
#
# New changes are synced by each service through its outbox (AUTHZ_SERVICE_URL);
# this covers what existed before. Safe to run again (existing tuples are kept).
# Switching a service to ReBAC: set AUTHZ_SERVICE_URL, run this, then AUTHZ_ENFORCE=rebac.
#
# Runs as the Postgres superuser of the compose stack: the note service's own
# role can't read every tenant's notes (row-level security), by design.
# Elsewhere, set PSQL to a command connecting with a role that has BYPASSRLS.
#
# Usage: scripts/authz-backfill.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PSQL=${PSQL:-"docker compose -f $ROOT/deploy/compose.yaml exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -q"}

export_tuples() {
	# columns: object_type, object_id, relation, subject_type, subject_id, tenant_id
	$PSQL -d iam -c "COPY (
		SELECT 'tenant', tenant_id, role, 'user', user_id, tenant_id
		FROM memberships WHERE removed_at IS NULL
	) TO STDOUT"
	$PSQL -d note -c "COPY (
		SELECT 'note', id::text, 'tenant', 'tenant', tenant_id, tenant_id FROM notes
		UNION ALL
		SELECT 'note', id::text, 'owner', 'user', created_by, tenant_id FROM notes
	) TO STDOUT"
}

load_tuples() {
	# a single psql session: the COPY data follows the command on stdin
	{
		cat <<'SQL'
BEGIN;
SET LOCAL app.actor = 'backfill';
CREATE TEMP TABLE staging (
    object_type text, object_id text, relation text,
    subject_type text, subject_id text, tenant_id text
) ON COMMIT DROP;
COPY staging FROM STDIN;
SQL
		cat
		cat <<'SQL'
\.
INSERT INTO relation_tuples (object_type, object_id, relation, subject_type, subject_id, subject_relation, tenant_id, created_by)
SELECT object_type, object_id, relation, subject_type, subject_id, '', tenant_id, 'backfill' FROM staging
ON CONFLICT DO NOTHING;
SELECT format('%s tuples read, %s new', (SELECT count(*) FROM staging),
              (SELECT count(*) FROM tuple_changes WHERE actor = 'backfill' AND changed_at >= now()));
COMMIT;
SQL
	} | $PSQL -d authz -At
}

export_tuples | load_tuples
