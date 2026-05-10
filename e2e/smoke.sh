#!/usr/bin/env bash
# End-to-end smoke test for the bigquery-emulator container image.
#
# Build the image from the top-level Dockerfile, verify that the
# binary's --version flag works inside the container, start the
# server, poll for readiness, hit a couple of REST endpoints, and
# tear down. Intended for ad-hoc local verification and as the
# manual checklist before tagging a release.
#
# Usage:
#   ./e2e/smoke.sh
#
# Optional env:
#   REVISION       — passed through to docker build as a build-arg.
#                    Defaults to the short SHA of HEAD.
#   READY_TIMEOUT  — seconds to wait for the HTTP port. Default 30.

set -euo pipefail

dir="$(cd "$(dirname "$0")" && pwd)"
cd "$dir"

REVISION="${REVISION:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
READY_TIMEOUT="${READY_TIMEOUT:-30}"
export REVISION

log() {
  printf "==> %s\n" "$*"
}

cleanup() {
  log "stopping emulator"
  docker compose down --volumes >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "building image (REVISION=${REVISION})"
docker compose build

log "verifying --version inside the image"
docker compose run --rm --no-deps bigquery-emulator --version

log "starting emulator"
docker compose up -d

log "waiting for http://localhost:9050 (timeout=${READY_TIMEOUT}s)"
ready=0
for _ in $(seq 1 "${READY_TIMEOUT}"); do
  # Any HTTP response (even 404) means the server is listening on the
  # port. We do not assume a specific endpoint shape so the script
  # stays compatible across emulator versions.
  if curl -sf -o /dev/null -m 1 "http://localhost:9050/" \
     || curl -s -o /dev/null -w '%{http_code}' -m 1 "http://localhost:9050/" 2>/dev/null \
        | grep -qE '^[1-5][0-9][0-9]$'; then
    ready=1
    break
  fi
  sleep 1
done

if [ "$ready" -ne 1 ]; then
  log "TIMEOUT waiting for emulator; recent logs:"
  docker compose logs --tail=50 bigquery-emulator >&2 || true
  exit 1
fi
log "ready"

log "probing discovery endpoint"
discovery_url="http://localhost:9050/discovery/v1/apis/bigquery/v2/rest"
discovery_status=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "${discovery_url}" || true)
log "discovery returned HTTP ${discovery_status}"
if [ "${discovery_status}" != "200" ]; then
  log "FAIL: discovery did not return 200"
  docker compose logs --tail=50 bigquery-emulator >&2 || true
  exit 1
fi

log "probing dataset list for project=test"
datasets_url="http://localhost:9050/projects/test/datasets"
datasets_status=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "${datasets_url}" || true)
log "datasets returned HTTP ${datasets_status}"
# Empty dataset list is fine — we just want a 2xx so we know the
# router and storage are wired up.
if [ "${datasets_status}" -lt 200 ] || [ "${datasets_status}" -ge 300 ]; then
  log "FAIL: dataset list did not return 2xx"
  docker compose logs --tail=50 bigquery-emulator >&2 || true
  exit 1
fi

log "SMOKE TEST PASSED"
