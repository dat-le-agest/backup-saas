-- Accounts of the platform's admin dashboard (not users of any app).

-- +goose Up
CREATE TABLE admins (
    id            varchar(64)  PRIMARY KEY,
    email         varchar(254) NOT NULL UNIQUE,
    password_hash varchar(255) NOT NULL,
    last_login_at timestamptz,
    created_at    timestamptz  NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE admins;
