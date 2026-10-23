-- Apps: every token is issued for one app (its audience) and every tenant
-- belongs to one app. Rows are upserted at startup from iam's --apps setting.
--
-- Data from before apps existed belongs to "notes", the only app at the time:
-- the app is created here only when there are tenants to assign to it.
-- Refresh tokens issued before are revoked (their access tokens had no app):
-- users sign in again.

-- +goose Up
CREATE TABLE apps (
    id         varchar(64) PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO apps (id) SELECT 'notes' WHERE EXISTS (SELECT 1 FROM tenants);

ALTER TABLE tenants ADD COLUMN app_id varchar(64) REFERENCES apps (id);
UPDATE tenants SET app_id = 'notes';
ALTER TABLE tenants ALTER COLUMN app_id SET NOT NULL;
CREATE INDEX idx_tenants_app_id ON tenants (app_id);

ALTER TABLE refresh_tokens ADD COLUMN app_id varchar(64) NOT NULL DEFAULT '';
ALTER TABLE refresh_tokens ALTER COLUMN app_id DROP DEFAULT;
UPDATE refresh_tokens SET revoked_at = now() WHERE revoked_at IS NULL;

-- +goose Down
ALTER TABLE refresh_tokens DROP COLUMN app_id;
DROP INDEX idx_tenants_app_id;
ALTER TABLE tenants DROP COLUMN app_id;
DROP TABLE apps;
