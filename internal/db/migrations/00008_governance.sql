-- +goose Up
-- Governance: approvals, change freezes and the Slack notification outbox.
-- (00007 belongs to another branch.)

CREATE TABLE approvals (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id       bigint      NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    manifest_id  text        NOT NULL DEFAULT '',
    requested_by bigint      NOT NULL REFERENCES users (id),
    requested_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    decided_by   bigint      REFERENCES users (id),
    decided_at   timestamptz,
    -- '' while the approval is open.
    decision     text        NOT NULL DEFAULT '' CHECK (decision IN ('', 'approved', 'rejected', 'expired', 'withdrawn')),
    comment      text        NOT NULL DEFAULT ''
);
-- One open approval per run; decided ones may repeat (a run can be asked again).
CREATE UNIQUE INDEX approvals_one_open_per_run_idx ON approvals (run_id) WHERE decision = '';
CREATE INDEX approvals_run_id_idx ON approvals (run_id, id DESC);

CREATE TABLE freezes (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    label       text        NOT NULL DEFAULT '',
    environment text        NOT NULL DEFAULT '',
    reason      text        NOT NULL,
    created_by  bigint      NOT NULL REFERENCES users (id),
    created_at  timestamptz NOT NULL,
    expires_at  timestamptz NOT NULL,
    lifted_by   bigint      REFERENCES users (id),
    lifted_at   timestamptz,
    -- Exactly one of label and environment names what is frozen.
    CONSTRAINT freezes_exactly_one_scope CHECK ((label <> '') <> (environment <> ''))
);
CREATE INDEX freezes_active_idx ON freezes (expires_at) WHERE lifted_at IS NULL;

CREATE TABLE notifications (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id          bigint      NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    kind            text        NOT NULL,
    channel         text        NOT NULL,
    payload         bytea       NOT NULL DEFAULT ''::bytea,
    state           text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sent', 'failed')),
    attempts        int         NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    -- A sender that claimed the row owns it until this time (see ClaimDueNotifications).
    claimed_until   timestamptz,
    last_error      text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz,
    UNIQUE (run_id, kind)
);
CREATE INDEX notifications_pending_idx ON notifications (id) WHERE state = 'pending';

-- +goose Down
DROP TABLE notifications;
DROP TABLE freezes;
DROP TABLE approvals;
