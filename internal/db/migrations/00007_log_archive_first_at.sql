-- +goose Up
-- The time of an archived leg's first line. A paged read uses it to leave a leg
-- undecoded until the page can reach it. Archives written before this have no
-- value (NULL) and are always decoded when they have lines left to read.
ALTER TABLE log_archives ADD COLUMN first_at timestamptz;

-- +goose Down
ALTER TABLE log_archives DROP COLUMN first_at;
