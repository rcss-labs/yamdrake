# Operating Yumdrake

How to run, upgrade, back up and repair a Yumdrake installation. Commands for
Docker Compose are run in `deploy/compose` (see its README for the first
setup). The ECS setup is described at the end.

## Upgrade

1. Read the release notes. Do not skip a release whose notes mention a breaking
   migration: upgrade through each one in order.
2. Back up first (see Backup and restore).
3. Change `YUMDRAKE_VERSION` in `.env` to the new tag, then:

   ```sh
   docker compose pull
   docker compose up -d
   ```

The orchestrator applies the schema migrations once, when it starts. The runners
start only after the orchestrator is healthy, and a runner also waits for the
schema version it needs. Migrations are backward compatible across one release,
so the old runners that are still draining during the switch keep working.

**Log archives are encrypted on the volume as of this release.** An archive
directory written by an earlier build (plain gzip) cannot be read. When upgrading
from a pre-release build, empty the log archive directory first:

```sh
docker compose stop orchestrator
docker run --rm -v yumdrake_log-archive:/v alpine:3.22.2 sh -c 'rm -rf /v/* /v/.[!.]*'
```

### Upgrade check

Recorded by the whole-system check.

## Backup and restore

What holds state: the `postgres-data` database, the `artifacts` volume (config
copies, encrypted, deleted after 30 days) and the `log-archive` volume
(encrypted). The `valkey-data` volume holds only a cache and wake-up signals and
needs no backup. **Also keep `YUMDRAKE_LOCAL_SECRET_KEY` (in `.env`) in a
separate safe place: without it the encrypted data cannot be read.**

Volume names are the Compose project name plus the volume: `yumdrake_artifacts`
and `yumdrake_log-archive`.

### Back up

```sh
docker compose exec -T postgres pg_dump -U yumdrake -Fc yumdrake > yumdrake-db.dump
for v in artifacts log-archive; do
  docker run --rm -v "yumdrake_$v:/v:ro" -v "$PWD:/b" alpine:3.22.2 \
    tar -czf "/b/yumdrake-$v.tar.gz" -C /v .
done
```

### Restore

Into a stopped application and an empty database:

```sh
docker compose stop orchestrator runner
docker compose exec -T postgres psql -U yumdrake -d postgres \
  -c 'DROP DATABASE yumdrake' -c 'CREATE DATABASE yumdrake'
docker compose exec -T postgres pg_restore -U yumdrake -d yumdrake --no-owner < yumdrake-db.dump
for v in artifacts log-archive; do
  docker run --rm -v "yumdrake_$v:/v" -v "$PWD:/b" alpine:3.22.2 \
    sh -c "rm -rf /v/* && tar -xzf /b/yumdrake-$v.tar.gz -C /v && chown -R 65532:65532 /v"
done
docker compose up -d
```

Use the same `YUMDRAKE_LOCAL_SECRET_KEY` that was in use when the backup was
taken.

## Restarts and reboots

All services restart with the host (`restart: unless-stopped`). Data lives on
named volumes, so `docker compose down` followed by `up -d` keeps runs, logs,
config copies and settings. Never use `down -v`: it deletes the volumes.

## First admin and locked accounts

Create the first admin as in the Compose README. An admin who locked themselves
out by failed sign-ins can be unlocked from the host:

```sh
docker compose exec -T orchestrator /yumdrake-orchestrator unlock --email admin@example.com
```

## Secrets in process arguments

Valkey is started with `--requirepass`, so its password is visible in the Valkey
container's process list. Anyone who can run `docker exec` or `docker top` on
this host can read it; treat access to the Docker socket as full access to
Yumdrake.

## Troubleshooting

| Symptom | Likely cause and fix |
| --- | --- |
| A service is `unhealthy` | `docker compose logs <service>`; most often a wrong password in `.env`. |
| Cannot sign in, "origin" error | `YUMDRAKE_PUBLIC_URL` differs from the browser address. |
| Admin is locked | Run `unlock` (above). |
| No certificate, Caddy ACME errors | DNS or ports 80/443; `docker compose logs caddy`. |
| Old log archives unreadable | Archives from a pre-release build are plain; see Upgrade. |
| After a restore, connections cannot be decrypted | Wrong `YUMDRAKE_LOCAL_SECRET_KEY`. |
| A run was in flight during a restart | It is resumed or reported as failed with its reason; check the run page. |

## ECS

For AWS (ECS Fargate with managed PostgreSQL and Valkey) see
[deploy/terraform/example/README.md](../deploy/terraform/example/README.md).
