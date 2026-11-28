-- +goose Up
-- Connected AWS accounts. Each account has two credentials that never share a
-- secret: discovery (read-only, used by the orchestrator) and deploy (used by
-- the runner). A secret access key is stored only as ciphertext, encrypted
-- under the purpose "aws-discovery" or "aws-deploy"; the access key id and role
-- ARN are not secret. It is numbered 00009 because it was renumbered to follow
-- 00008 (governance) when the branches were merged.
CREATE TABLE aws_accounts (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text        NOT NULL UNIQUE CHECK (name <> ''),
    account_id text        NOT NULL UNIQUE CHECK (account_id ~ '^[0-9]{12}$'),

    discovery_method            text  NOT NULL CHECK (discovery_method IN ('role', 'key')),
    discovery_role_arn          text  NOT NULL DEFAULT '',
    discovery_external_id       text  NOT NULL DEFAULT '',
    discovery_access_key_id     text  NOT NULL DEFAULT '',
    discovery_secret_ciphertext bytea,

    deploy_method            text  NOT NULL CHECK (deploy_method IN ('role', 'key')),
    deploy_role_arn          text  NOT NULL DEFAULT '',
    deploy_external_id       text  NOT NULL DEFAULT '',
    deploy_access_key_id     text  NOT NULL DEFAULT '',
    deploy_secret_ciphertext bytea,

    created_at timestamptz NOT NULL DEFAULT now(),

    CHECK ((discovery_method = 'role' AND discovery_role_arn <> '' AND discovery_secret_ciphertext IS NULL)
        OR (discovery_method = 'key' AND discovery_access_key_id <> '' AND discovery_secret_ciphertext IS NOT NULL)),
    CHECK ((deploy_method = 'role' AND deploy_role_arn <> '' AND deploy_secret_ciphertext IS NULL)
        OR (deploy_method = 'key' AND deploy_access_key_id <> '' AND deploy_secret_ciphertext IS NOT NULL))
);

-- +goose Down
DROP TABLE aws_accounts;
