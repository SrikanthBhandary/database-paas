-- +goose Up
ALTER TABLE databases
    ADD COLUMN connection_host              text    NOT NULL DEFAULT '',
    ADD COLUMN connection_port              integer NOT NULL DEFAULT 0 CHECK (connection_port >= 0),
    ADD COLUMN connection_database          text    NOT NULL DEFAULT '',
    ADD COLUMN connection_username          text    NOT NULL DEFAULT '',
    ADD COLUMN credentials_secret_namespace text    NOT NULL DEFAULT '',
    ADD COLUMN credentials_secret_name      text    NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE databases
    DROP COLUMN connection_host,
    DROP COLUMN connection_port,
    DROP COLUMN connection_database,
    DROP COLUMN connection_username,
    DROP COLUMN credentials_secret_namespace,
    DROP COLUMN credentials_secret_name;
