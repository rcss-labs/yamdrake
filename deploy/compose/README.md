# Yumdrake on one host with Docker Compose

This runs Yumdrake (orchestrator, runners, PostgreSQL, Valkey and an HTTPS front)
on a single machine with only Docker installed. It uses the same images as the
ECS setup and needs no AWS service of its own. Run every command below in this
directory (`deploy/compose`).

## 1. Prerequisites

- Docker Engine with Docker Compose v2.24 or newer (`docker compose version`).
- A DNS name (for example `yumdrake.example.com`) that points at this host.
- Ports 80 and 443 open to the internet (or at least to Let's Encrypt for the
  certificate check on port 80). For a host that is not reachable from the
  internet, see "Without the HTTPS front" below.
- `openssl` to generate secrets.

## 2. Fill in `.env`

```sh
cp .env.example .env
chmod 600 .env
```

Edit `.env` and replace every `<placeholder>`:

- `YUMDRAKE_PUBLIC_HOST` is the DNS name; `YUMDRAKE_PUBLIC_URL` is
  `https://` plus the same name. They must match.
- `YUMDRAKE_VERSION` is the release tag to run (for example `v1.0.0`).
- Generate each secret and paste it in:
  - `openssl rand -hex 24` for `YUMDRAKE_DATABASE_PASSWORD` and again for
    `YUMDRAKE_VALKEY_PASSWORD`.
  - `openssl rand -base64 32` for `YUMDRAKE_LOCAL_SECRET_KEY`.

**Back up `YUMDRAKE_LOCAL_SECRET_KEY` somewhere safe, apart from the data.**
It encrypts stored connections, config copies and log archives. If it is lost,
those are unreadable, and changing it later has the same effect. `.env` is
git-ignored; never commit it.

## 3. Start

```sh
docker compose up -d --wait
docker compose ps
```

`.env` sets `COMPOSE_FILE=compose.yaml:compose.tls.yaml`, so this starts the
Caddy front too. Caddy fetches a certificate for your host name on first start
(this can take a minute). Every service must show `healthy`. If one does not,
see Troubleshooting.

## 4. Create the first admin

The password is read from the terminal and never appears on a command line:

```sh
read -rp "Email: " ADMIN_EMAIL
read -rp "Name: " ADMIN_NAME
read -rs -p "Password: " ADMIN_PASSWORD; echo
printf '%s\n' "$ADMIN_PASSWORD" | docker compose exec -T orchestrator \
  /yumdrake-orchestrator create-admin --email "$ADMIN_EMAIL" --name "$ADMIN_NAME"
unset ADMIN_PASSWORD
```

## 5. Sign in and set up

Open `https://<your host name>` and sign in with the admin. Then, in the web
application:

1. Connect your AWS accounts (the AWS credentials of this host are described
   under "AWS access" below).
2. Connect GitHub.
3. Set the inventory source.

## AWS access

Yumdrake's two services use the AWS SDK default credential chain, so there are
two supported ways on a single host:

- **An instance role** on the host (for example an EC2 instance profile).
  Leave all `YUMDRAKE_*_AWS_*` variables in `.env` empty. Both services then
  share the host's role, so the role must be allowed to assume the discovery
  roles and the deployment roles. Prefer the second way when you can: it keeps
  the two identities apart.
- **An access key per service**, each for a dedicated IAM user, in `.env`:
  `YUMDRAKE_ORCHESTRATOR_AWS_ACCESS_KEY_ID` and `..._SECRET_ACCESS_KEY` for the
  orchestrator, `YUMDRAKE_RUNNER_AWS_ACCESS_KEY_ID` and `..._SECRET_ACCESS_KEY`
  for the runners. The orchestrator's user may assume only discovery roles; the
  runner's user may assume only deployment roles. Never give both services the
  same user.

`YUMDRAKE_AWS_REGION` and `YUMDRAKE_AWS_ENDPOINT_URL` are optional. Empty values
are ignored by the SDK, so leaving a variable empty is safe.

## Without the HTTPS front

For a host behind your own proxy or load balancer, set
`COMPOSE_FILE=compose.yaml` in `.env`. The orchestrator then publishes
`YUMDRAKE_HTTP_BIND:YUMDRAKE_HTTP_PORT` (default `127.0.0.1:8080`), and
`YUMDRAKE_PUBLIC_URL` must be the address people use through your proxy
(`https://` is required except for `localhost`). Make the proxy pass the event
stream through unbuffered.

## Upgrade, backup, restore

See [../../docs/operations.md](../../docs/operations.md). In short: change
`YUMDRAKE_VERSION`, then `docker compose pull && docker compose up -d`.

## Logs

```sh
docker compose logs -f orchestrator
docker compose logs --tail 100 runner caddy
```

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `required variable ... is missing a value` | A variable in `.env` is unset. Compare with `.env.example`. |
| `docker compose` rejects `!reset` | Compose is older than v2.24. Upgrade Docker. |
| The browser warns about the certificate, or Caddy logs an ACME error | The DNS name does not point at this host, or ports 80/443 are not reachable. Check `docker compose logs caddy`. |
| Sign-in fails with an origin error | `YUMDRAKE_PUBLIC_URL` does not match the address in the browser. |
| `orchestrator` stays `starting` or unhealthy | `docker compose logs orchestrator`. A wrong database or Valkey password shows there. |
| Runners wait for the schema | The orchestrator has not finished its migrations; wait, or read its log. |
| Encrypted data cannot be read after a restore | `YUMDRAKE_LOCAL_SECRET_KEY` is not the key used when the data was written. |
