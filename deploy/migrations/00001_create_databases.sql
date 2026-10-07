-- +goose Up
CREATE TABLE databases (
    id                          uuid        PRIMARY KEY,
    name                        text        NOT NULL,
    engine                      text        NOT NULL CHECK (engine IN ('postgres', 'mysql')),
    version                     text        NOT NULL,
    plan                        text        NOT NULL,
    status                      text        NOT NULL
        CHECK (status IN ('pending', 'provisioning', 'ready', 'failed', 'deleting')),
    status_reason               text        NOT NULL DEFAULT '',
    owner_id                    text        NOT NULL,

    cpu_millicores              integer     NOT NULL CHECK (cpu_millicores > 0),
    memory_mb                   integer     NOT NULL CHECK (memory_mb > 0),
    storage_gb                  integer     NOT NULL CHECK (storage_gb > 0),
    replicas                    integer     NOT NULL DEFAULT 0 CHECK (replicas >= 0),

    autoscale_enabled           boolean     NOT NULL DEFAULT false,
    autoscale_max_storage_gb    integer     NOT NULL DEFAULT 0 CHECK (autoscale_max_storage_gb >= 0),

    backup_enabled              boolean     NOT NULL DEFAULT false,
    backup_schedule             text        NOT NULL DEFAULT '',
    backup_retention_days       integer     NOT NULL DEFAULT 0 CHECK (backup_retention_days >= 0),

    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now()
);

-- the DB-level guarantee behind ErrAlreadyExists
CREATE UNIQUE INDEX databases_owner_name_key ON databases (owner_id, name);

-- the worker will scan by status
CREATE INDEX databases_status_idx ON databases (status);

-- +goose Down
DROP TABLE databases;
