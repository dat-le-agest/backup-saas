-- +goose Up
CREATE TABLE notes (
    id         bigserial    PRIMARY KEY,
    tenant_id  varchar(64)  NOT NULL,
    created_by varchar(64)  NOT NULL,
    title      varchar(255) NOT NULL,
    content    text         NOT NULL DEFAULT '',
    created_at timestamptz  NOT NULL DEFAULT now(),
    updated_at timestamptz  NOT NULL DEFAULT now()
);
-- "list notes of a tenant, newest first"
CREATE INDEX idx_notes_tenant_id_id ON notes (tenant_id, id);

-- Second layer of tenant isolation (the first is tenancy.Scope in the repository):
-- rows are only visible and writable when app.tenant_id, set per transaction by
-- core/tenancy.Tx, matches. FORCE applies it to the table owner (the service's role).
ALTER TABLE notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE notes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notes
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- +goose Down
DROP TABLE notes;
