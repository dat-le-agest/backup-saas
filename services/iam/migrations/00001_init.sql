-- iam spans tenants by design (a user's tenant list, login by email), so its
-- tables don't use row-level security; the service passes tenant ids explicitly.

-- +goose Up
CREATE TABLE tenants (
    id         varchar(64)  PRIMARY KEY,
    name       varchar(255) NOT NULL,
    created_at timestamptz  NOT NULL DEFAULT now(),
    updated_at timestamptz  NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            varchar(64)  PRIMARY KEY,
    -- NULL for users created by dev login; several NULLs are allowed by UNIQUE
    email         varchar(254) UNIQUE,
    password_hash varchar(255) NOT NULL DEFAULT '',
    created_at    timestamptz  NOT NULL DEFAULT now(),
    updated_at    timestamptz  NOT NULL DEFAULT now()
);

-- never deleted: removal sets removed_at so version keeps increasing
CREATE TABLE memberships (
    tenant_id  varchar(64) NOT NULL REFERENCES tenants (id),
    user_id    varchar(64) NOT NULL REFERENCES users (id),
    role       varchar(32) NOT NULL,
    version    bigint      NOT NULL,
    removed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);
CREATE INDEX idx_memberships_user_id ON memberships (user_id);

-- failed logins per email (registered or not)
CREATE TABLE login_throttles (
    email        varchar(254) PRIMARY KEY,
    failures     integer      NOT NULL,
    locked_until timestamptz,
    updated_at   timestamptz  NOT NULL DEFAULT now()
);

CREATE TABLE refresh_tokens (
    id         bigserial   PRIMARY KEY,
    token_hash varchar(64) NOT NULL UNIQUE,
    family_id  varchar(64) NOT NULL,
    user_id    varchar(64) NOT NULL,
    tenant_id  varchar(64) NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_refresh_tokens_family_id ON refresh_tokens (family_id);
CREATE INDEX idx_refresh_tokens_user_tenant ON refresh_tokens (user_id, tenant_id);

-- +goose Down
DROP TABLE refresh_tokens;
DROP TABLE login_throttles;
DROP TABLE memberships;
DROP TABLE users;
DROP TABLE tenants;
