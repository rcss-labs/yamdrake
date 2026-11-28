#!/usr/bin/env bash
# Removes the floci-aws-* sibling containers (ECS tasks, the ECR registry) that
# OUR Floci created.
#
# Another Floci may run on the same Docker daemon and also create floci-aws-*
# containers, so the name is not an ownership marker. Our Floci's containers
# are attached to the Compose network of our Floci container, which is found
# by the host port it publishes. Floci's shared VPC networks (floci-aws-vpc-*)
# are not a marker (both Flocis use the same name) and are ignored.
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/floci-clean.sh

Environment:
  YUMDRAKE_TEST_AWS_ENDPOINT  URL of our Floci (default http://localhost:4577)

Exit codes: 0 done (or nothing to do), 2 Docker is unavailable.
USAGE
}

if [[ ${1:-} == "-h" || ${1:-} == "--help" ]]; then
  usage
  exit 0
fi

command -v docker >/dev/null 2>&1 || { echo "docker is not on PATH" >&2; exit 2; }

endpoint="${YUMDRAKE_TEST_AWS_ENDPOINT:-http://localhost:4577}"
port="${endpoint##*:}"
port="${port%%/*}"

floci_id="$(docker ps -q --filter "publish=${port}" --filter "ancestor=floci/floci:2.5.0" | head -n 1)"
if [[ -z "$floci_id" ]]; then
  echo "floci-clean: no Floci of ours publishes port ${port}; nothing to clean"
  exit 0
fi

networks="$(docker inspect --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}' "$floci_id" \
  | grep -Ev '^(bridge|host|none|floci-aws-vpc-.*)?$' || true)"
if [[ -z "$networks" ]]; then
  echo "floci-clean: cannot tell which network our Floci is on; refusing to guess" >&2
  exit 1
fi

removed=0
while IFS= read -r network; do
  ids="$(docker ps -aq --filter "name=floci-aws-" --filter "network=${network}")"
  if [[ -n "$ids" ]]; then
    # shellcheck disable=SC2086 # ids are container ids, one per word
    docker rm -f $ids >/dev/null
    removed=$((removed + $(wc -w <<<"$ids")))
  fi
done <<<"$networks"
echo "floci-clean: removed ${removed} container(s) on our Floci's network"
