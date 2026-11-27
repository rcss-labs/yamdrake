-- +goose Up
-- People, groups, grants, API tokens and the audit log.
--
-- The audit table is append-only. The application role must be granted INSERT
-- and SELECT on it and never UPDATE, DELETE or TRUNCATE:
--   GRANT INSERT, SELECT ON audit_log TO <application role>;
-- No store function modifies or deletes audit rows.

CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         text        NOT NULL CHECK (email <> '' AND email = lower(email)),
    name          text        NOT NULL DEFAULT '',
    source        text        NOT NULL CHECK (source IN ('saml', 'local', 'service')),
    password_hash text,
    disabled      boolean     NOT NULL DEFAULT false,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

CREATE TABLE groups (
    id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name  text    NOT NULL CHECK (name <> ''),
    admin boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX groups_name_lower_key ON groups (lower(name));

-- A user may be a member of one group both manually and through the identity
-- provider: those are two rows, so replacing one kind never removes the other.
CREATE TABLE group_members (
    user_id  bigint NOT NULL REFERENCES users (id),
    group_id bigint NOT NULL REFERENCES groups (id),
    source   text   NOT NULL CHECK (source IN ('manual', 'idp')),
    PRIMARY KEY (user_id, group_id, source)
);
CREATE INDEX group_members_group_id_idx ON group_members (group_id);

CREATE TABLE idp_group_mappings (
    idp_group text   PRIMARY KEY CHECK (idp_group <> ''),
    group_id  bigint NOT NULL REFERENCES groups (id)
);

-- label and environment are '' for "any"; folder_id is NULL for "any".
CREATE TABLE grants (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    group_id    bigint NOT NULL REFERENCES groups (id),
    action      text   NOT NULL CHECK (action IN
        ('view_logs', 'run', 'cancel', 'approve', 'restore',
         'manage_jobs', 'manage_connections', 'manage_access', 'view_audit')),
    label       text   NOT NULL DEFAULT '' CHECK (label IN ('', 'dev', 'preprod', 'prod')),
    environment text   NOT NULL DEFAULT '',
    folder_id   bigint REFERENCES folders (id),
    CHECK (label = '' OR environment = ''),
    CHECK (action NOT IN ('manage_jobs', 'manage_connections', 'manage_access', 'view_audit')
           OR (label = '' AND environment = '' AND folder_id IS NULL))
);
CREATE UNIQUE INDEX grants_unique_key
    ON grants (group_id, action, label, environment, (coalesce(folder_id, 0)));

CREATE TABLE api_tokens (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint      NOT NULL REFERENCES users (id),
    name         text        NOT NULL,
    hash         bytea       NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id);

CREATE TABLE audit_log (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at          timestamptz NOT NULL,
    actor_id    bigint REFERENCES users (id),
    actor       text        NOT NULL DEFAULT '',
    action      text        NOT NULL,
    target_type text        NOT NULL DEFAULT '',
    target_id   text        NOT NULL DEFAULT '',
    detail      jsonb
);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE api_tokens;
DROP TABLE grants;
DROP TABLE idp_group_mappings;
DROP TABLE group_members;
DROP TABLE groups;
DROP TABLE users;
