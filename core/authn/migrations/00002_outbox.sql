-- Transactional outbox (core/outbox.MigrationSQL) in the app's authentication
-- database: account mail (mail.Queue) is stored in the same transaction as the
-- change it is about, and delivered after commit by the gateway's relay.

-- +goose Up
CREATE TABLE outbox_messages (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic           text        NOT NULL,
    key             text        NOT NULL DEFAULT '',
    payload         jsonb       NOT NULL,
    headers         jsonb       NOT NULL DEFAULT '{}',
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    processed_at    timestamptz,
    failed_at       timestamptz
);
-- pending messages, in delivery order
CREATE INDEX outbox_messages_pending ON outbox_messages (id)
    WHERE processed_at IS NULL AND failed_at IS NULL;
-- cleanup of delivered messages
CREATE INDEX outbox_messages_processed_at ON outbox_messages (processed_at)
    WHERE processed_at IS NOT NULL;

-- +goose Down
DROP TABLE outbox_messages;
