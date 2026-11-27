-- +goose Up
-- The last failure to reach GitHub, shown next to the (unchanged) definitions.
ALTER TABLE inventory_status ADD COLUMN sync_error text, ADD COLUMN sync_error_at timestamptz;
ALTER TABLE job_sources ADD COLUMN sync_error text, ADD COLUMN sync_error_at timestamptz;

-- +goose Down
ALTER TABLE job_sources DROP COLUMN sync_error_at, DROP COLUMN sync_error;
ALTER TABLE inventory_status DROP COLUMN sync_error_at, DROP COLUMN sync_error;
