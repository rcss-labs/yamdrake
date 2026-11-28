-- +goose Up
-- One system event per throttling answer AWS gave to the orchestrator or a
-- runner; the AWSThrottles metric counts them.
ALTER TABLE system_events DROP CONSTRAINT system_events_kind_check;
ALTER TABLE system_events ADD CONSTRAINT system_events_kind_check
    CHECK (kind IN ('lease_expired', 'log_persist_failed', 'sync_failed', 'aws_throttled'));

-- +goose Down
DELETE FROM system_events WHERE kind = 'aws_throttled';
ALTER TABLE system_events DROP CONSTRAINT system_events_kind_check;
ALTER TABLE system_events ADD CONSTRAINT system_events_kind_check
    CHECK (kind IN ('lease_expired', 'log_persist_failed', 'sync_failed'));
