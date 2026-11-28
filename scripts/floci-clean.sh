#!/usr/bin/env bash
# Removes the floci-aws-* sibling containers (ECS tasks, the ECR registry) that
# OUR Floci created.
#
# Another Floci may run on the same Docker daemon and also create floci-aws-*
# containers, so the name is not an ownership marker. Our Floci's containers
# are attached to the Compose network of our Floci container, which is found
# by the host port it publishes. Floci's shared VPC networks (floci-aws-vpc-*)
# are not a marker (both Flocis use the same name) and are ignored.
#
# With --network NAME the Floci is the one of that Compose network instead (the
# demo's Floci is published on 127.0.0.1:18566, 18666 in demo-check, so the port
# above does not find it; it is loopback only, an unauthenticated emulator with
# the Docker socket, never to be exposed further): only the
# floci-aws-* containers attached to exactly that network are removed.
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/floci-clean.sh [--network NAME]

  --network NAME  remove the floci-aws-* containers attached to the Compose
                  network NAME (for example yumdrake-demo_default) and nothing else

Environment:
  YUMDRAKE_TEST_AWS_ENDPOINT  URL of our Floci (default http://localhost:4577)

Exit codes: 0 done (or nothing to do), 2 Docker is unavailable.
USAGE
}

if [[ ${1:-} == "-h" || ${1:-} == "--help" ]]; then
  usage
  exit 0
fi

network_arg=""
if [[ ${1:-} == "--network" ]]; then
  network_arg="${2:-}"
  if [[ -z "$network_arg" || $# -gt 2 ]]; then
    usage >&2
    exit 2
  fi
elif [[ $# -gt 0 ]]; then
  usage >&2
  exit 2
fi

command -v docker >/dev/null 2>&1 || { echo "docker is not on PATH" >&2; exit 2; }

if [[ -n "$network_arg" ]]; then
  # Exactly this network: never the VPC networks, never another project's.
  ids="$(docker ps -aq --filter "name=floci-aws-" --filter "network=${network_arg}")"
  if [[ -n "$ids" ]]; then
    # shellcheck disable=SC2086 # ids are container ids, one per word
    docker rm -f $ids >/dev/null
  fi
  echo "floci-clean: removed $(wc -w <<<"$ids" | tr -d ' ') container(s) on network ${network_arg}"
  exit 0
fi

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
