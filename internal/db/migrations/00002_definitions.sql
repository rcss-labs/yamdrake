-- +goose Up
CREATE TABLE github_connections (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner             text        NOT NULL UNIQUE,
    method            text        NOT NULL CHECK (method IN ('app', 'token')),
    app_id            bigint      NOT NULL DEFAULT 0,
    installation_id   bigint      NOT NULL DEFAULT 0,
    secret_ciphertext bytea       NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE folders (
    id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL
);
CREATE UNIQUE INDEX folders_name_lower_key ON folders (lower(name));

CREATE TABLE job_sources (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    folder_id        bigint      NOT NULL REFERENCES folders (id),
    repo             text        NOT NULL,
    branch           text        NOT NULL,
    path             text        NOT NULL,
    job_id           text        NOT NULL DEFAULT '',
    title            text        NOT NULL DEFAULT '',
    last_good        jsonb,
    last_good_commit text        NOT NULL DEFAULT '',
    runnable         boolean     NOT NULL DEFAULT false,
    problems         jsonb,
    problems_commit  text        NOT NULL DEFAULT '',
    checked_at       timestamptz,
    synced_at        timestamptz,
    UNIQUE (repo, branch, path)
);
-- A job id is unique across sources once it is known (empty means not yet read).
CREATE UNIQUE INDEX job_sources_job_id_key ON job_sources (job_id) WHERE job_id <> '';
CREATE INDEX job_sources_folder_id_idx ON job_sources (folder_id);

CREATE TABLE inventory_snapshots (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    commit_sha   text        NOT NULL,
    inventory    jsonb       NOT NULL,
    active       boolean     NOT NULL DEFAULT false,
    activated_at timestamptz NOT NULL
);
-- At most one active snapshot.
CREATE UNIQUE INDEX inventory_snapshots_one_active ON inventory_snapshots (active) WHERE active;

-- Single row; also the lock row that serialises snapshot activation.
CREATE TABLE inventory_status (
    id              smallint    PRIMARY KEY CHECK (id = 1),
    checked_at      timestamptz,
    problems        jsonb,
    problems_commit text        NOT NULL DEFAULT ''
);
INSERT INTO inventory_status (id) VALUES (1);

-- +goose Down
DROP TABLE inventory_status;
DROP TABLE inventory_snapshots;
DROP TABLE job_sources;
DROP TABLE folders;
DROP TABLE github_connections;
