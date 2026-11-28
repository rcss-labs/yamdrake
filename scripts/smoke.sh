#!/usr/bin/env bash
# Smoke test: build and start the Compose `app` profile, then verify the
# orchestrator is ready and both service containers report healthy.
set -euo pipefail

usage() {
	cat <<'USAGE'
Usage: scripts/smoke.sh [-h]

Builds and starts the Compose "app" profile (postgres, valkey, orchestrator,
runner) under its own Compose project (yumdrake-smoke) on host ports that do
not clash with `make dev-up`. The runner runs here, and only here, with
YUMDRAKE_ENV=dev and YUMDRAKE_FAKE_STEPS=true. Checks that /readyz reports
status "ok", that /api/v1/me answers 401 without credentials (sign-in is wired
in), that / serves the embedded web application (200, text/html, the root
element and the X-Frame-Options: DENY header), that both service containers
are healthy and that the runner registered the fake steps. Only the smoke
project's containers and volumes are removed on exit, so a running development
stack is untouched.

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
# The smoke stack alone runs the runner with fake steps (dev environment);
# compose.yaml keeps both off by default.
export YUMDRAKE_RUNNER_ENV=dev
export YUMDRAKE_RUNNER_FAKE_STEPS=true

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

echo "smoke: checking that /api/v1/me refuses a request without credentials"
me_status="$(curl -s --max-time 10 -o /dev/null -w '%{http_code}' "http://localhost:${orchestrator_port}/api/v1/me" || true)"
if [ "$me_status" != "401" ]; then
	echo "smoke: FAIL: /api/v1/me answered ${me_status:-no response} without credentials, want 401" >&2
	exit 1
fi

echo "smoke: checking that / serves the web application"
root_marker='<div id="root">'
root_headers="$(mktemp)"
root_body="$(curl -s --max-time 10 -D "$root_headers" "http://localhost:${orchestrator_port}/" || true)"
root_status="$(awk 'NR==1 {print $2}' "$root_headers" | tr -d '\r')"
root_type="$(grep -i '^content-type:' "$root_headers" | tr -d '\r' || true)"
root_frame="$(grep -i '^x-frame-options:' "$root_headers" | tr -d '\r' || true)"
rm -f "$root_headers"
if [ "$root_status" != "200" ]; then
	echo "smoke: FAIL: / answered ${root_status:-no response}, want 200 (is the web stage missing from the image?)" >&2
	exit 1
fi
if ! grep -qi 'text/html' <<<"$root_type"; then
	echo "smoke: FAIL: / has content type '${root_type:-none}', want text/html" >&2
	exit 1
fi
if ! grep -qF "$root_marker" <<<"$root_body"; then
	echo "smoke: FAIL: / does not contain ${root_marker}" >&2
	exit 1
fi
if ! grep -qi '^x-frame-options:[[:space:]]*DENY$' <<<"$root_frame"; then
	echo "smoke: FAIL: / has X-Frame-Options '${root_frame:-none}', want DENY" >&2
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

echo "smoke: checking that the runner registered the fake steps"
runner_logs="$(compose logs runner 2>&1 || true)"
if ! grep -q 'fake steps are registered' <<<"$runner_logs"; then
	echo "smoke: FAIL: the runner did not register the fake steps (YUMDRAKE_ENV=dev, YUMDRAKE_FAKE_STEPS=true)" >&2
	exit 1
fi

echo "smoke: PASS"
