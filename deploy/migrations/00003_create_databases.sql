-- +goose Up
ALTER TABLE databases DROP CONSTRAINT databases_status_check;
ALTER TABLE databases ADD CONSTRAINT databases_status_check CHECK (status IN (
    'pending', 'provisioning', 'ready', 'failed', 'deleting',
    'update_pending', 'updating', 'update_failed'));

-- +goose Down
-- fails if rows with the new statuses exist; clear or migrate them first
ALTER TABLE databases DROP CONSTRAINT databases_status_check;
ALTER TABLE databases ADD CONSTRAINT databases_status_check CHECK (status IN (
    'pending', 'provisioning', 'ready', 'failed', 'deleting'));
