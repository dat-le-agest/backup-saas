-- Same schema as deploy/postgres/authz/init.sql (documented there), as the
-- service's first goose migration. Keep later changes in new migrations.

-- +goose Up
-- =============================================================================
-- Centralized authorization store (ReBAC, Zanzibar / OpenFGA style)
-- =============================================================================
--
-- Database: authz, owned by role authz (deploy/postgres/init.sql).
-- Apply:    psql "postgres://authz:authz@localhost:5432/authz" -f deploy/postgres/authz/init.sql
-- Becomes migrations/00001_init.sql of the authz service (add goose annotations).
--
-- Concepts
--   object   "note:42"            object_type:object_id
--   relation "viewer"             defined per type in the authorization model
--   subject  "user:alice"         a concrete subject
--            "team:eng#member"    a userset: everyone with relation member on team:eng
--            "user:*"             wildcard: every user (public access)
--   tuple    object#relation@subject, e.g. note:42#viewer@team:eng#member
--
-- Everything else (who can do what) is computed from tuples by the authorization
-- model; nothing here encodes permissions directly.
--
-- Example model for the kit (DSL, stored in authorization_models.dsl):
--
--   model
--     schema 1.1
--   type user
--   type tenant
--     relations
--       define owner: [user]
--       define admin: [user] or owner
--       define member: [user] or admin
--       define viewer: [user] or member
--   type team
--     relations
--       define tenant: [tenant]
--       define member: [user]
--   type note
--     relations
--       define tenant: [tenant]
--       define owner: [user]
--       define editor: [user, team#member] or owner or admin from tenant
--       define viewer: [user, user:*, team#member] or editor or viewer from tenant
--       define can_view: viewer
--       define can_edit: editor
--       define can_delete: owner or admin from tenant
--
-- How today's RBAC maps to tuples
--   iam.memberships (tnt_a, usr_1, admin)  →  tenant:tnt_a#admin@user:usr_1
--   notes.tenant_id = tnt_a                →  note:42#tenant@tenant:tnt_a
--   notes.created_by = usr_1               →  note:42#owner@user:usr_1
--   share a note with a team               →  note:42#editor@team:eng#member
--   public link                            →  note:42#viewer@user:*
--
-- Tenancy: the store is cross-tenant by design (guests and sharing cross tenant
-- boundaries), so there is no row-level security. Each tuple records the tenant
-- that owns its object, to export or delete a tenant's data.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- Authorization models: immutable versions; exactly one is active.
-- -----------------------------------------------------------------------------
CREATE TABLE authorization_models (
    id             text        COLLATE "C" PRIMARY KEY,      -- ULID, sortable by creation
    schema_version text        NOT NULL DEFAULT '1.1',
    dsl            text        NOT NULL,                     -- source, as written by people
    model          jsonb       NOT NULL,                     -- compiled type definitions used by the checker
    is_active      boolean     NOT NULL DEFAULT false,
    created_by     text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT authorization_models_id_ulid CHECK (id ~ '^[0-9A-HJKMNP-TV-Z]{26}$'),
    CONSTRAINT authorization_models_model_object CHECK (jsonb_typeof(model) = 'object')
);

-- at most one active model; checks use it unless a request pins another version
CREATE UNIQUE INDEX authorization_models_one_active
    ON authorization_models ((true)) WHERE is_active;

-- models are append-only: a change is a new version
-- +goose StatementBegin
CREATE FUNCTION authorization_models_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.dsl IS DISTINCT FROM OLD.dsl OR NEW.model IS DISTINCT FROM OLD.model
       OR NEW.schema_version IS DISTINCT FROM OLD.schema_version THEN
        RAISE EXCEPTION 'authorization model % is immutable: create a new version', OLD.id;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd

CREATE TRIGGER authorization_models_immutable
    BEFORE UPDATE ON authorization_models
    FOR EACH ROW EXECUTE FUNCTION authorization_models_immutable();

-- Test cases for a model ("user:alice can_view note:42 = true"), run before activating it.
CREATE TABLE authorization_model_assertions (
    model_id   text        COLLATE "C" PRIMARY KEY REFERENCES authorization_models (id) ON DELETE CASCADE,
    assertions jsonb       NOT NULL DEFAULT '[]',
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT authorization_model_assertions_array CHECK (jsonb_typeof(assertions) = 'array')
);

-- -----------------------------------------------------------------------------
-- Relationship tuples: the current state. Immutable rows (write = insert, revoke = delete).
-- -----------------------------------------------------------------------------
CREATE TABLE relation_tuples (
    object_type       text        COLLATE "C" NOT NULL,
    object_id         text        COLLATE "C" NOT NULL,
    relation          text        COLLATE "C" NOT NULL,

    subject_type      text        COLLATE "C" NOT NULL,
    subject_id        text        COLLATE "C" NOT NULL,          -- '*' = every subject of subject_type
    subject_relation  text        COLLATE "C" NOT NULL DEFAULT '', -- '' = the subject itself, else a userset

    tenant_id         text        COLLATE "C" NOT NULL,          -- tenant owning the object

    -- conditional tuple (ABAC on top of ReBAC): granted only if the named condition
    -- of the model evaluates to true with this context + the request context
    condition_name    text        COLLATE "C",
    condition_context jsonb,

    created_by        text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),

    -- also serves Check: WHERE object_type = ? AND object_id = ? AND relation = ? [AND subject...]
    PRIMARY KEY (object_type, object_id, relation, subject_type, subject_id, subject_relation),

    CONSTRAINT relation_tuples_object_type    CHECK (object_type  ~ '^[a-z][a-z0-9_]{0,63}$'),
    CONSTRAINT relation_tuples_relation       CHECK (relation     ~ '^[a-z][a-z0-9_]{0,63}$'),
    CONSTRAINT relation_tuples_subject_type   CHECK (subject_type ~ '^[a-z][a-z0-9_]{0,63}$'),
    CONSTRAINT relation_tuples_subject_rel    CHECK (subject_relation = '' OR subject_relation ~ '^[a-z][a-z0-9_]{0,63}$'),
    -- ids: 1-256 chars, no whitespace or '#' (separator); '*' only as a subject wildcard
    -- (length checked apart: Postgres regex repetition counts stop at 255)
    CONSTRAINT relation_tuples_object_id      CHECK (char_length(object_id) <= 256 AND object_id ~ '^[^\s#*]+$'),
    CONSTRAINT relation_tuples_subject_id     CHECK (char_length(subject_id) <= 256 AND subject_id ~ '^[^\s#]+$'),
    -- a wildcard is "every user", never "every user's relation"
    CONSTRAINT relation_tuples_wildcard       CHECK (subject_id <> '*' OR subject_relation = ''),
    CONSTRAINT relation_tuples_tenant_id      CHECK (tenant_id <> ''),
    CONSTRAINT relation_tuples_condition      CHECK (condition_context IS NULL OR condition_name IS NOT NULL),
    CONSTRAINT relation_tuples_condition_ctx  CHECK (condition_context IS NULL OR jsonb_typeof(condition_context) = 'object')
);

-- Userset expansion during Check: "which usersets hold relation R on this object?"
-- (team:eng#member, tenant:t1#admin...). Partial: direct subjects are the large majority.
CREATE INDEX relation_tuples_usersets
    ON relation_tuples (object_type, object_id, relation)
    INCLUDE (subject_type, subject_id, subject_relation)
    WHERE subject_relation <> '';

-- Reverse lookups: ListObjects ("notes alice can view"), "everything a team is
-- granted", and cleaning up a deleted user or team.
CREATE INDEX relation_tuples_by_subject
    ON relation_tuples (subject_type, subject_id, subject_relation, object_type, relation, object_id);

-- Tenant export / deletion.
CREATE INDEX relation_tuples_by_tenant
    ON relation_tuples (tenant_id, object_type);

-- -----------------------------------------------------------------------------
-- Changelog: append-only history of every tuple write and delete.
--   * ReadChanges / watch: consumers (caches, search indexes) poll by seq
--   * revision for consistency tokens: max(seq) at the time of a write
--   * audit: who granted or revoked what, when
-- Filled by trigger, so it can't miss a change even for manual SQL fixes.
-- -----------------------------------------------------------------------------
CREATE TABLE tuple_changes (
    seq               bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operation         text        NOT NULL,

    object_type       text        COLLATE "C" NOT NULL,
    object_id         text        COLLATE "C" NOT NULL,
    relation          text        COLLATE "C" NOT NULL,
    subject_type      text        COLLATE "C" NOT NULL,
    subject_id        text        COLLATE "C" NOT NULL,
    subject_relation  text        COLLATE "C" NOT NULL,
    tenant_id         text        COLLATE "C" NOT NULL,
    condition_name    text        COLLATE "C",
    condition_context jsonb,

    -- set by the authz service per transaction: SET LOCAL app.actor / app.request_id
    actor             text        NOT NULL DEFAULT '',
    request_id        text        NOT NULL DEFAULT '',
    changed_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT tuple_changes_operation CHECK (operation IN ('write', 'delete'))
);

-- ReadChanges filtered by type: WHERE object_type = ? AND seq > ? ORDER BY seq
CREATE INDEX tuple_changes_by_type ON tuple_changes (object_type, seq);
-- audit per tenant
CREATE INDEX tuple_changes_by_tenant ON tuple_changes (tenant_id, seq);
-- retention (DELETE ... WHERE changed_at < now() - interval '...'): cheap on append-only data
CREATE INDEX tuple_changes_changed_at ON tuple_changes USING brin (changed_at);

-- +goose StatementBegin
CREATE FUNCTION relation_tuples_log_change() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    t relation_tuples;
    op text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        t := NEW; op := 'write';
    ELSE
        t := OLD; op := 'delete';
    END IF;

    INSERT INTO tuple_changes (
        operation, object_type, object_id, relation,
        subject_type, subject_id, subject_relation, tenant_id,
        condition_name, condition_context, actor, request_id
    ) VALUES (
        op, t.object_type, t.object_id, t.relation,
        t.subject_type, t.subject_id, t.subject_relation, t.tenant_id,
        t.condition_name, t.condition_context,
        coalesce(current_setting('app.actor', true), ''),
        coalesce(current_setting('app.request_id', true), '')
    );
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER relation_tuples_log_change
    AFTER INSERT OR DELETE ON relation_tuples
    FOR EACH ROW EXECUTE FUNCTION relation_tuples_log_change();

-- Tuples are facts: to change one (e.g. its condition), delete and insert in one
-- transaction, so the changelog shows both.
-- +goose StatementBegin
CREATE FUNCTION relation_tuples_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'relation tuples are immutable: delete and insert instead';
END $$;
-- +goose StatementEnd

CREATE TRIGGER relation_tuples_immutable
    BEFORE UPDATE ON relation_tuples
    FOR EACH ROW EXECUTE FUNCTION relation_tuples_immutable();

-- +goose Down
DROP TABLE tuple_changes;
DROP TABLE relation_tuples;
DROP FUNCTION relation_tuples_log_change();
DROP FUNCTION relation_tuples_immutable();
DROP TABLE authorization_model_assertions;
DROP TABLE authorization_models;
DROP FUNCTION authorization_models_immutable();
