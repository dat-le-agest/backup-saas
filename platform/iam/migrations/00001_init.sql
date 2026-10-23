-- Shared iam tables, in the default schema: apps and users are the same for
-- every app. Each app's tenants, memberships and refresh tokens live in the
-- app's own schema (migrations/app, applied per app at startup).

-- +goose Up
-- apps served by the platform; rows are added from PLATFORM_APPS at startup,
-- together with the app's schema
CREATE TABLE apps (
    id         varchar(64) PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT apps_id CHECK (id ~ '^[a-z][a-z0-9-]{0,62}$')
);

-- one account for every app
CREATE TABLE users (
    id            varchar(64)  PRIMARY KEY,
    -- NULL for users created by dev login; several NULLs are allowed by UNIQUE
    email         varchar(254) UNIQUE,
    password_hash varchar(255) NOT NULL DEFAULT '',
    created_at    timestamptz  NOT NULL DEFAULT now(),
    updated_at    timestamptz  NOT NULL DEFAULT now()
);

-- failed logins per email (registered or not)
CREATE TABLE login_throttles (
    email        varchar(254) PRIMARY KEY,
    failures     integer      NOT NULL,
    locked_until timestamptz,
    updated_at   timestamptz  NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE login_throttles;
DROP TABLE users;
DROP TABLE apps;
