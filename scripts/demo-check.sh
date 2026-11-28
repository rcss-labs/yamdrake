#!/usr/bin/env bash
# Demo check: runs the demo (deploy/compose/compose.demo.yaml) for real, through the API and the CLI.
set -euo pipefail

usage() {
	cat <<'USAGE'
Usage: scripts/demo-check.sh [-h]

Builds and starts the demo under its own Compose project (yumdrake-demo-check)
on host port ${YUMDRAKE_DEMO_CHECK_PORT:-18380}, with the public demo settings
of deploy/compose/demo/demo.env. Only that project is ever torn down; the
development stack, the demo you may be running (project yumdrake-demo) and
other projects are never touched. Checks:
  - the stack comes up healthy, which means the demo helper has seeded the
    demo's local AWS (Floci) and Yumdrake
  - the admin, the deployer and the viewer each sign in through the API
  - the viewer is refused when planning a run (403) and the deployer is not
  - the version lists (the version picker's data) hold the seeded versions
  - a failure: the deployer deploys a version of the API whose image crashes;
    the run ends partly succeeded and names the failed service and its exit code
  - a restore: restoring that run puts the services back on the old version,
    checked in the local AWS through the running versions
  - a deployment: the CLI, built from this checkout and signed in as the
    deployer, plans and runs a job with --yes, and the services run the new
    version in the local AWS
  - the browser test (web/e2e/demo.spec.ts) signs in, picks a version in the
    picker, reads the plan, starts a run and watches it succeed
On exit the floci-aws-* containers this demo's Floci started (found by the
project's Compose network, never another Floci's) are removed, then the project
with `down -v --remove-orphans`, and the script checks that nothing remains.

Needs Docker, curl, python3, go, and for the browser test node with the web
dependencies and Chromium (make web-install; npx playwright install chromium).

Environment (default): YUMDRAKE_DEMO_CHECK_PORT=18380.

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

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if ! docker info >/dev/null 2>&1; then
	echo "demo-check: Docker is not available" >&2
	exit 3
fi
for tool in curl python3 go npx; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "demo-check: $tool is required" >&2
		exit 1
	fi
done

project="yumdrake-demo-check"
# The Compose network of the project: every floci-aws-* container this demo's Floci starts is attached to it.
network="${project}_default"
registry_name="floci-aws-demo-check-ecr-registry"
port="${YUMDRAKE_DEMO_CHECK_PORT:-18380}"
compose_dir="$repo_root/deploy/compose"
base_url="http://localhost:$port"

port_in_use() {
	if command -v lsof >/dev/null 2>&1; then
		lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
	else
		(exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
	fi
}
if port_in_use "$port"; then
	echo "demo-check: FAIL: host port $port is already in use; set YUMDRAKE_DEMO_CHECK_PORT" >&2
	exit 1
fi

work_dir="$(mktemp -d)"
env_file="$work_dir/demo.env"
failures=0
started=0

# The public demo settings, with this check's own port. The last assignment in an env file wins.
grep -vE '^(YUMDRAKE_HTTP_PORT|YUMDRAKE_PUBLIC_URL|YUMDRAKE_IMAGE_REPO|YUMDRAKE_VERSION)=' "$compose_dir/demo/demo.env" >"$env_file"
check_images=(
	yumdrake-demo-check/yumdrake-orchestrator:check
	yumdrake-demo-check/yumdrake-runner:check
	yumdrake-demo-check/yumdrake-demo:check
)
{
	echo "YUMDRAKE_IMAGE_REPO=yumdrake-demo-check"
	echo "YUMDRAKE_VERSION=check"
	echo "YUMDRAKE_HTTP_PORT=$port"
	echo "YUMDRAKE_PUBLIC_URL=$base_url"
	# This check's Floci registry has its own name and port range, so a demo you run yourself is not disturbed.
	echo "YUMDRAKE_DEMO_REGISTRY_NAME=$registry_name"
	echo "YUMDRAKE_DEMO_REGISTRY_PORT_FROM=5400"
	echo "YUMDRAKE_DEMO_REGISTRY_PORT_TO=5499"
} >>"$env_file"

compose() {
	docker compose -p "$project" --project-directory "$compose_dir" --env-file "$env_file" \
		-f "$compose_dir/compose.yaml" -f "$compose_dir/compose.demo.yaml" "$@"
}

pass() { echo "demo-check: PASS: $1"; }
fail() {
	echo "demo-check: FAIL: $1" >&2
	failures=$((failures + 1))
}
# check "description" command...   (runs the command, records PASS or FAIL)
check() {
	local what="$1"
	shift
	if "$@" >/dev/null 2>&1; then
		pass "$what"
	else
		fail "$what"
	fi
}
# abort stops the run at once: later checks would only repeat the same failure.
abort() {
	fail "$1"
	if [ "$started" -eq 1 ]; then
		compose ps >&2 || true
		compose logs --tail 40 >&2 || true
	fi
	exit 1
}

cleanup() {
	local status=$?
	# Only this project, and only here. The floci-aws-* containers of this demo's Floci (its ECS tasks and its
	# registry) are not Compose's: they are removed first, by this project's network and the registry's own name.
	"$repo_root/scripts/floci-clean.sh" --network "$network" >/dev/null 2>&1 || true
	docker rm -f "$registry_name" >/dev/null 2>&1 || true
	local tasks_left
	tasks_left="$(docker ps -a --filter "name=floci-aws-" --filter "network=$network" -q 2>/dev/null | wc -l | tr -d ' ')"
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	# The images carry this check's own tags (set in the env file), so `make demo`
	# images are never touched.
	docker rmi "${check_images[@]}" >/dev/null 2>&1 || true
	rm -rf "$work_dir"
	local left
	left="$(docker ps -a --filter "label=com.docker.compose.project=$project" -q 2>/dev/null | wc -l | tr -d ' ')"
	# What Floci started: the tasks were counted on the project's network before `down` removed it.
	left=$((left + $(docker ps -a --filter "name=$registry_name" -q 2>/dev/null | wc -l | tr -d ' ')))
	left=$((left + tasks_left))
	if [ "$left" = "0" ]; then
		echo "demo-check: PASS: no container of project $project remains"
	else
		echo "demo-check: FAIL: $left container(s) of project $project remain" >&2
		status=1
	fi
	exit "$status"
}
trap cleanup EXIT

# json_field <field> reads a JSON object on standard input and prints one top-level field.
json_field() {
	python3 -c 'import json,sys
print(json.load(sys.stdin)[sys.argv[1]])' "$1"
}

# sign_in <cookie-jar> <email> <password>: 0 when the API sets a session cookie.
sign_in() {
	local jar="$1" body code
	body="$(printf '{"email":"%s","password":"%s"}' "$2" "$3")"
	rm -f "$jar"
	code="$(curl -sS -o /dev/null -w '%{http_code}' -c "$jar" \
		-H "Origin: $base_url" -H 'Content-Type: application/json' \
		-d "$body" "$base_url/api/v1/auth/login")" || return 1
	[ "$code" = "200" ] && grep -q yumdrake_session "$jar"
}

# status_of <cookie-jar> <method> <path> <json-body>: prints the HTTP status.
status_of() {
	curl -sS -o /dev/null -w '%{http_code}' -b "$1" -X "$2" \
		-H "Origin: $base_url" -H 'Content-Type: application/json' \
		-d "$4" "$base_url$3"
}

# --- loopback only ----------------------------------------------------------
# The demo uses public credentials, so it must publish on 127.0.0.1 even when the
# environment asks for more.
published_host_ip() {
	YUMDRAKE_HTTP_BIND=0.0.0.0 compose config --format json |
		python3 -c 'import json,sys
cfg=json.load(sys.stdin)
for port in cfg["services"]["orchestrator"].get("ports", []):
    print(port.get("host_ip", ""))'
}
host_ips="$(published_host_ip 2>/dev/null)" || host_ips=""
if [ "$host_ips" = "127.0.0.1" ]; then
	pass "the orchestrator publishes only on 127.0.0.1, even with YUMDRAKE_HTTP_BIND=0.0.0.0"
else
	fail "the orchestrator's published host_ip is '$(echo "$host_ips" | tr '\n' ' ')' with YUMDRAKE_HTTP_BIND=0.0.0.0, want only 127.0.0.1"
	exit 1
fi

# --- start ------------------------------------------------------------------
echo "demo-check: building and starting the demo (project $project, port $port)"
started=1
compose up -d --build --wait --wait-timeout 600 >/dev/null 2>&1 || abort "the demo did not become healthy"
pass "the demo is healthy (the helper has seeded it)"

admin_jar="$work_dir/admin.jar"
deployer_jar="$work_dir/deployer.jar"
viewer_jar="$work_dir/viewer.jar"
check "the admin signs in through the API" sign_in "$admin_jar" admin@demo.yumdrake.test demo-admin-password
check "the deployer signs in through the API" sign_in "$deployer_jar" deployer@demo.yumdrake.test demo-deployer-password
check "the viewer signs in through the API" sign_in "$viewer_jar" viewer@demo.yumdrake.test demo-viewer-password
[ "$failures" -eq 0 ] || abort "a demo user could not sign in; the checks that follow need all three"

# --- permissions ------------------------------------------------------------
job_id() {
	curl -fsS -b "$admin_jar" "$base_url/api/v1/jobs" |
		python3 -c 'import json,sys
for job in json.load(sys.stdin)["jobs"]:
    if job["job_id"] == sys.argv[1]:
        print(job["id"])' "$1"
}
all_job="$(job_id deploy-all)" || all_job=""
[ -n "$all_job" ] || abort "the job deploy-all is not in the demo"

# The selection: 1.4.2 for the group, with the API overridden to 1.4.3, whose image crashes.
crash_selection='{"all":{"version":"1.4.2","services":{"web":"","api":"1.4.3","worker":""}}}'
plan_body="{\"environment\":\"dev\",\"selection\":$crash_selection,\"start\":false}"

viewer_status="$(status_of "$viewer_jar" POST "/api/v1/jobs/$all_job/runs" "$plan_body")" || viewer_status="error"
if [ "$viewer_status" = "403" ]; then
	pass "the viewer is refused when planning a run (403)"
else
	fail "the viewer planning a run got status $viewer_status, want 403"
fi
planned_run="$(curl -sS -w '\n%{http_code}' -b "$deployer_jar" -X POST \
	-H "Origin: $base_url" -H 'Content-Type: application/json' \
	-d "$plan_body" "$base_url/api/v1/jobs/$all_job/runs")" || planned_run=""
deployer_status="${planned_run##*$'\n'}"
if [ "$deployer_status" = "201" ]; then
	pass "the deployer may plan a run in dev (201)"
else
	fail "the deployer planning a run got status $deployer_status, want 201"
fi
planned_run_id="$(printf '%s' "${planned_run%$'\n'*}" | json_field id 2>/dev/null)" || planned_run_id=""
if [ -z "$planned_run_id" ]; then
	fail "no planned run to try starting as the viewer"
else
	viewer_start="$(status_of "$viewer_jar" POST "/api/v1/runs/$planned_run_id/start" '{}')" || viewer_start="error"
	if [ "$viewer_start" = "403" ]; then
		pass "the viewer is refused when starting a planned run (403)"
	else
		fail "the viewer starting a planned run got status $viewer_start, want 403"
	fi
	# Throw the plan away: it must not stand in the way of the runs below.
	status_of "$deployer_jar" POST "/api/v1/runs/$planned_run_id/cancel" '' >/dev/null || true
fi

# --- helpers for runs and the local AWS ---------------------------------------
# api_get <jar> <path>: the JSON body of a GET.
api_get() { curl -fsS -b "$1" "$base_url$2"; }

# api_post <jar> <path> <json-body>: the JSON body of a POST that must succeed.
api_post() {
	curl -fsS -b "$1" -X POST -H "Origin: $base_url" -H 'Content-Type: application/json' -d "$3" "$base_url$2"
}

# wait_run_state <run-id> <seconds> <state>...: prints the state the run reached, when it is one of the states
# given (a final state) or when the time is up.
wait_run_state() {
	local id="$1" limit="$2" state="" want
	shift 2
	local deadline=$((SECONDS + limit))
	while [ "$SECONDS" -lt "$deadline" ]; do
		state="$(api_get "$deployer_jar" "/api/v1/runs/$id" | json_field state 2>/dev/null)" || state=""
		for want in "$@"; do
			if [ "$state" = "$want" ]; then
				echo "$state"
				return 0
			fi
		done
		sleep 2
	done
	echo "$state"
	return 1
}

# running_version <environment> <service>: the version the service runs in that environment in the local AWS,
# read fresh through the orchestrator (the admin may refresh).
running_version() {
	api_get "$admin_jar" "/api/v1/versions/running?refresh=1" |
		python3 -c 'import json,sys
matrix = json.load(sys.stdin)
env, service = sys.argv[1], sys.argv[2]
names = [e["name"] for e in matrix["environments"]]
for row in matrix["rows"]:
    if row["service"] == service:
        print(row["cells"][names.index(env)]["version"])' "$1" "$2"
}

# --- the picker's data ------------------------------------------------------
versions_json="$(api_get "$deployer_jar" "/api/v1/jobs/$all_job/versions?environment=dev")" || versions_json=""
common="$(printf '%s' "$versions_json" | python3 -c 'import json,sys
print(" ".join(json.load(sys.stdin)["groups"]["all"]["common"]))' 2>/dev/null)" || common=""
api_tags="$(printf '%s' "$versions_json" | python3 -c 'import json,sys
print(" ".join(sorted(v["tag"] for v in json.load(sys.stdin)["groups"]["all"]["services"]["api"])))' 2>/dev/null)" || api_tags=""
if [ "$common" = "1.4.2 1.4.1" ] && [ "$api_tags" = "1.4.1 1.4.2 1.4.3" ]; then
	pass "the version lists hold the seeded versions (common: $common; api also has 1.4.3)"
else
	fail "the version lists are wrong: common '$common', api tags '$api_tags'; want '1.4.2 1.4.1' and '1.4.1 1.4.2 1.4.3'"
fi
for service in web api worker; do
	if [ "$(running_version dev "$service" 2>/dev/null)" != "1.4.1" ]; then
		abort "$service does not run 1.4.1 in dev in the local AWS before the first deployment"
	fi
done
pass "web, api and worker run 1.4.1 in dev in the local AWS"

# --- a failure: the API's crashing version ----------------------------------------
fail_run="$(api_post "$deployer_jar" "/api/v1/jobs/$all_job/runs" "{\"environment\":\"dev\",\"selection\":$crash_selection,\"start\":true}" | json_field id)" ||
	abort "the deployer could not start the run with the crashing version"
fail_state="$(wait_run_state "$fail_run" 420 succeeded partially_succeeded failed cancelled)" || true
if [ "$fail_state" = "partially_succeeded" ]; then
	pass "the run with the crashing version ended partly succeeded"
else
	fail "the run with the crashing version is '$fail_state', want partially_succeeded"
fi
failed_services="$(api_get "$deployer_jar" "/api/v1/runs/$fail_run" | python3 -c 'import json,sys
run = json.load(sys.stdin)
print(" ".join(f["service"] + ":" + f["detail"] for f in run["summary"]["failures"]))' 2>/dev/null)" || failed_services=""
if [[ "$failed_services" == api:*"exited with 3"* ]] && [[ "$failed_services" != *web:* ]] && [[ "$failed_services" != *worker:* ]]; then
	pass "the failure names the service api and its exit code 3"
else
	fail "the failures are '$failed_services', want only api, with 'exited with 3'"
fi
for service in web worker; do
	if [ "$(running_version dev "$service" 2>/dev/null)" = "1.4.2" ]; then
		pass "$service runs 1.4.2 in dev in the local AWS, as the failed run left it"
	else
		fail "$service does not run 1.4.2 in dev in the local AWS after the run that failed in api"
	fi
done

# --- a restore ------------------------------------------------------------------
restore_run="$(api_post "$deployer_jar" "/api/v1/runs/$fail_run/restore" '{}' | json_field id)" || restore_run=""
if [ -z "$restore_run" ]; then
	fail "the deployer could not plan a restore of the failed run"
else
	wait_run_state "$restore_run" 120 planned failed >/dev/null || true
	api_post "$deployer_jar" "/api/v1/runs/$restore_run/start" '{}' >/dev/null || fail "the restore run could not be started"
	restore_state="$(wait_run_state "$restore_run" 420 succeeded partially_succeeded failed cancelled)" || true
	if [ "$restore_state" = "succeeded" ]; then
		pass "the restore run succeeded"
	else
		fail "the restore run is '$restore_state', want succeeded"
	fi
	for service in web worker; do
		if [ "$(running_version dev "$service" 2>/dev/null)" = "1.4.1" ]; then
			pass "$service is back on 1.4.1 in dev in the local AWS"
		else
			fail "$service did not return to 1.4.1 in dev in the local AWS"
		fi
	done
fi

# --- a deployment, through the CLI ------------------------------------------
cli="$work_dir/yumdrake"
go build -o "$cli" ./cmd/yumdrake || abort "the CLI did not build"
pass "the CLI builds"

# A token made through the session API, as the web app's token page does. It is handed to the CLI
# through the environment and never printed.
token="$(curl -fsS -b "$deployer_jar" -H "Origin: $base_url" -H 'Content-Type: application/json' \
	-d '{"name":"demo-check","ttl_days":1}' "$base_url/api/v1/tokens" | json_field plaintext)" ||
	abort "the deployer could not create a CLI token"
cli_run() {
	# HOME is empty so that no real config file is read or written.
	HOME="$work_dir" YUMDRAKE_SERVER="$base_url" YUMDRAKE_TOKEN="$token" NO_COLOR=1 "$cli" "$@"
}

jobs_out="$(cli_run jobs list 2>&1)" || jobs_out=""
if [[ "$jobs_out" == *deploy-all* ]]; then
	pass "the CLI lists the demo jobs"
else
	fail "the CLI did not list deploy-all"
fi

versions=(--set all=1.4.2)
if cli_run plan deploy-all --env dev "${versions[@]}" >"$work_dir/plan.out" 2>&1; then
	pass "the CLI plans deploy-all in dev"
else
	fail "the CLI could not plan deploy-all in dev: $(tail -n 3 "$work_dir/plan.out" | tr '\n' ' ')"
fi

limit=()
if command -v timeout >/dev/null 2>&1; then
	limit=(timeout 420)
fi
if ${limit[@]+"${limit[@]}"} env HOME="$work_dir" YUMDRAKE_SERVER="$base_url" YUMDRAKE_TOKEN="$token" NO_COLOR=1 \
	"$cli" run deploy-all --env dev "${versions[@]}" --yes --watch >"$work_dir/run.out" 2>&1; then
	pass "the CLI ran deploy-all in dev and it succeeded"
else
	fail "the run did not succeed: $(tail -n 5 "$work_dir/run.out" | tr '\n' ' ')"
fi
for service in web api worker; do
	if [ "$(running_version dev "$service" 2>/dev/null)" = "1.4.2" ]; then
		pass "$service runs 1.4.2 in dev in the local AWS"
	else
		fail "$service does not run 1.4.2 in dev in the local AWS after the deployment"
	fi
done

# --- the browser ------------------------------------------------------------
if [ "$failures" -eq 0 ]; then
	if (cd "$repo_root/web" && YUMDRAKE_DEMO_URL="$base_url" npx playwright test -c playwright.demo.config.ts >"$work_dir/browser.out" 2>&1); then
		pass "the browser test signed in, picked a version, read the plan, started a run and it succeeded"
	else
		fail "the browser test failed: $(tail -n 25 "$work_dir/browser.out" | tr '\n' ' ')"
	fi
else
	echo "demo-check: skipping the browser test: earlier checks failed" >&2
fi

if [ "$failures" -ne 0 ]; then
	echo "demo-check: $failures check(s) failed" >&2
	exit 1
fi
echo "demo-check: all checks passed"
