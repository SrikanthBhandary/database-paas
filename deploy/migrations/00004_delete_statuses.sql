-- +goose Up
ALTER TABLE databases DROP CONSTRAINT databases_status_check;
ALTER TABLE databases ADD CONSTRAINT databases_status_check CHECK (status IN (
    'pending', 'provisioning', 'ready', 'failed',
    'update_pending', 'updating', 'update_failed',
    'delete_pending', 'deleting', 'delete_failed'));

-- +goose Down
-- fails if rows in the new statuses exist; clear them first
ALTER TABLE databases DROP CONSTRAINT databases_status_check;
ALTER TABLE databases ADD CONSTRAINT databases_status_check CHECK (status IN (
    'pending', 'provisioning', 'ready', 'failed', 'deleting',
    'update_pending', 'updating', 'update_failed'));
