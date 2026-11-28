-- +goose Up
-- The run engine: runs, their steps and legs, operations, locks and log lines.
-- (00005 is reserved for another branch.)

CREATE TABLE runs (
    id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind                 text        NOT NULL CHECK (kind IN ('deploy', 'restore')),
    job_source_id        bigint      NOT NULL REFERENCES job_sources (id),
    job_id               text        NOT NULL,
    job_snapshot         jsonb       NOT NULL,
    inventory_commit     text        NOT NULL DEFAULT '',
    job_commit           text        NOT NULL DEFAULT '',
    environment          text        NOT NULL DEFAULT '',
    label                text        NOT NULL DEFAULT '',
    aws_account          text        NOT NULL DEFAULT '',
    folder_id            bigint      NOT NULL REFERENCES folders (id),
    selection            jsonb       NOT NULL,
    manifest             jsonb,
    manifest_id          text        NOT NULL DEFAULT '',
    approval_required    boolean     NOT NULL DEFAULT false,
    auto_start           boolean     NOT NULL DEFAULT false,
    state                text        NOT NULL CHECK (state IN
        ('resolving', 'planned', 'waiting_approval', 'queued', 'running', 'cancelling',
         'needs_reconciliation', 'succeeded', 'partially_succeeded', 'failed', 'cancelled',
         'rejected', 'expired', 'invalidated')),
    wait_reason          text        NOT NULL DEFAULT '',
    requested_by         bigint      NOT NULL REFERENCES users (id),
    via                  text        NOT NULL CHECK (via IN ('web', 'cli')),
    parent_run_id        bigint      REFERENCES runs (id),
    cancel_requested_by  bigint      REFERENCES users (id),
    cancel_requested_at  timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    state_changed_at     timestamptz NOT NULL DEFAULT now(),
    started_at           timestamptz,
    finished_at          timestamptz
);
CREATE INDEX runs_job_source_id_idx ON runs (job_source_id, id DESC);
CREATE INDEX runs_environment_idx ON runs (environment, id DESC);
-- Partial index: the orchestrator sweeps the (small) set of unfinished runs.
CREATE INDEX runs_active_idx ON runs (id) WHERE state NOT IN
    ('succeeded', 'partially_succeeded', 'failed', 'cancelled', 'rejected', 'expired', 'invalidated');

CREATE TABLE run_steps (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id       bigint NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    name         text   NOT NULL,
    use          text   NOT NULL,
    group_id     text   NOT NULL DEFAULT '',
    position     int    NOT NULL,
    max_parallel int    NOT NULL DEFAULT 0,
    timeout_ns   bigint NOT NULL DEFAULT 0,
    needs        jsonb  NOT NULL DEFAULT '[]',
    UNIQUE (run_id, position)
);

CREATE TABLE legs (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id           bigint      NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    step_id          bigint      REFERENCES run_steps (id) ON DELETE CASCADE,
    kind             text        NOT NULL CHECK (kind IN ('resolve', 'revalidate', 'step')),
    service          text        NOT NULL DEFAULT '',
    version          text        NOT NULL DEFAULT '',
    state            text        NOT NULL CHECK (state IN
        ('pending', 'queued', 'claimed', 'running', 'succeeded', 'failed', 'skipped',
         'cancelled', 'unknown')),
    attempt          int         NOT NULL DEFAULT 0,
    generation       bigint      NOT NULL DEFAULT 0,
    lease_expiries   integer     NOT NULL DEFAULT 0,
    runner_id        text        NOT NULL DEFAULT '',
    lease_until      timestamptz,
    cancel_requested boolean     NOT NULL DEFAULT false,
    resume           jsonb,
    result           jsonb,
    failure_short    text        NOT NULL DEFAULT '',
    failure_detail   text        NOT NULL DEFAULT '',
    queued_at        timestamptz,
    started_at       timestamptz,
    finished_at      timestamptz
);
CREATE INDEX legs_run_id_idx ON legs (run_id);
CREATE INDEX legs_claim_idx ON legs (state, queued_at);
CREATE INDEX legs_lease_idx ON legs (lease_until) WHERE state IN ('claimed', 'running');

-- One row per external change a leg intends to make. (leg_id, key) is the
-- idempotency key: a retry of the same change finds its earlier row.
CREATE TABLE operations (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    leg_id  bigint      NOT NULL REFERENCES legs (id) ON DELETE CASCADE,
    seq     int         NOT NULL,
    name    text        NOT NULL,
    key     text        NOT NULL,
    intent  jsonb,
    result  jsonb,
    state   text        NOT NULL CHECK (state IN ('intended', 'done', 'failed')),
    at      timestamptz NOT NULL,
    UNIQUE (leg_id, key),
    UNIQUE (leg_id, seq)
);

-- One row per held resource; the primary key is the lock.
CREATE TABLE run_locks (
    resource    text        PRIMARY KEY CHECK (resource <> ''),
    run_id      bigint      NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    acquired_at timestamptz NOT NULL
);
CREATE INDEX run_locks_run_id_idx ON run_locks (run_id);

-- leg_id 0 holds the lines the orchestrator writes about the run itself, so
-- there is no foreign key on leg_id.
CREATE TABLE log_lines (
    run_id bigint      NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    leg_id bigint      NOT NULL,
    seq    bigint      NOT NULL,
    at     timestamptz NOT NULL,
    level  text        NOT NULL CHECK (level IN ('info', 'ok', 'warn', 'error')),
    text   text        NOT NULL,
    PRIMARY KEY (run_id, leg_id, seq)
);

-- Where a leg's lines went once archived to object storage.
CREATE TABLE log_archives (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id      bigint      NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    leg_id      bigint      NOT NULL,
    key         text        NOT NULL,
    line_count  bigint      NOT NULL,
    first_seq   bigint      NOT NULL,
    last_seq    bigint      NOT NULL,
    archived_at timestamptz NOT NULL,
    UNIQUE (run_id, leg_id)
);

-- +goose Down
DROP TABLE log_archives;
DROP TABLE log_lines;
DROP TABLE run_locks;
DROP TABLE operations;
DROP TABLE legs;
DROP TABLE run_steps;
DROP TABLE runs;
