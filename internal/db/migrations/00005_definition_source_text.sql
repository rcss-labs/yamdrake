-- +goose Up
-- The raw YAML of the last good version, stored in the same write as its
-- parsed form so the text and the parsed form always come from one commit.
-- Rows written before this migration have no text; the API shows "".
ALTER TABLE job_sources ADD COLUMN last_good_source text NOT NULL DEFAULT '';
-- Maps each file read for the snapshot (relative to the inventory path, for
-- example services/orders.yaml) to its text.
ALTER TABLE inventory_snapshots ADD COLUMN sources jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE inventory_snapshots DROP COLUMN sources;
ALTER TABLE job_sources DROP COLUMN last_good_source;
