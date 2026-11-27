#!/usr/bin/env bash
# Smoke test: build and start the Compose `app` profile, then verify the
# orchestrator is ready and both service containers report healthy.
set -euo pipefail

usage() {
	cat <<'USAGE'
Usage: scripts/smoke.sh [-h]

Builds and starts the Compose "app" profile (postgres, valkey, orchestrator,
runner) under its own Compose project (yumdrake-smoke) on host ports that do
not clash with `make dev-up`, checks that /readyz reports status "ok" and that
both service containers are healthy. Only the smoke project's containers and
volumes are removed on exit, so a running development stack is untouched.

Environment (defaults): YUMDRAKE_SMOKE_ORCHESTRATOR_PORT=18080,
YUMDRAKE_SMOKE_POSTGRES_PORT=15432, YUMDRAKE_SMOKE_VALKEY_PORT=16379.

Exit codes:
  0  all checks passed
  1  a check failed
  2  usage error
  3  Docker is not available
USAGE
}

while getopts ":h" opt; do
	case "$opt" in
	h)
		usage
		exit 0
		;;
	*)
		usage >&2
		exit 2
		;;
	esac
done
if [ "$OPTIND" -le "$#" ]; then
	usage >&2
	exit 2
fi

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if ! docker info >/dev/null 2>&1; then
	echo "smoke: Docker is not available" >&2
	exit 3
fi

project="yumdrake-smoke"
orchestrator_port="${YUMDRAKE_SMOKE_ORCHESTRATOR_PORT:-18080}"
export YUMDRAKE_ORCHESTRATOR_PORT="$orchestrator_port"
export YUMDRAKE_POSTGRES_PORT="${YUMDRAKE_SMOKE_POSTGRES_PORT:-15432}"
export YUMDRAKE_VALKEY_PORT="${YUMDRAKE_SMOKE_VALKEY_PORT:-16379}"

compose() {
	docker compose -p "$project" --profile app "$@"
}

cleanup() {
	compose down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "smoke: building and starting the app profile"
if ! compose up -d --build --wait --wait-timeout 120; then
	echo "smoke: FAIL: the app profile did not start healthy" >&2
	compose ps >&2 || true
	exit 1
fi

echo "smoke: checking /readyz"
readyz=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
	if readyz="$(curl -fsS "http://localhost:${orchestrator_port}/readyz" 2>/dev/null)"; then
		break
	fi
	sleep 1
done
if ! grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"' <<<"$readyz"; then
	echo "smoke: FAIL: /readyz did not report status ok: ${readyz:-no response}" >&2
	exit 1
fi

for service in orchestrator runner; do
	id="$(compose ps -q "$service")"
	if [ -z "$id" ]; then
		echo "smoke: FAIL: no container for $service" >&2
		exit 1
	fi
	health="$(docker inspect -f '{{.State.Health.Status}}' "$id")"
	if [ "$health" != "healthy" ]; then
		echo "smoke: FAIL: $service is $health, want healthy" >&2
		exit 1
	fi
	echo "smoke: $service is healthy"
done

echo "smoke: PASS"
