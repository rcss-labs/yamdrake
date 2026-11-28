-- +goose Up
-- A notification can now be about something other than a run (a break-glass
-- sign-in). Such rows have no run; the (run_id, kind) uniqueness still holds
-- for run rows because NULLs are distinct.
ALTER TABLE notifications ALTER COLUMN run_id DROP NOT NULL;

-- +goose Down
DELETE FROM notifications WHERE run_id IS NULL;
ALTER TABLE notifications ALTER COLUMN run_id SET NOT NULL;
