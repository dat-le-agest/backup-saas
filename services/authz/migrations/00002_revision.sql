-- +goose Up
-- Revision of the tuple store, for caching Check results: cached answers are
-- keyed by it, so any tuple change makes them unused. Incremented by a trigger in
-- the writing transaction; the row lock orders increments like commits (unlike
-- tuple_changes.seq, assigned at insert time and visible out of order).
CREATE TABLE authz_revision (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    value     bigint  NOT NULL
);
INSERT INTO authz_revision (value) VALUES (0);

-- +goose StatementBegin
CREATE FUNCTION relation_tuples_bump_revision() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE authz_revision SET value = value + 1;
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER relation_tuples_bump_revision
    AFTER INSERT OR DELETE ON relation_tuples
    FOR EACH STATEMENT EXECUTE FUNCTION relation_tuples_bump_revision();

-- +goose Down
DROP TRIGGER relation_tuples_bump_revision ON relation_tuples;
DROP FUNCTION relation_tuples_bump_revision();
DROP TABLE authz_revision;
