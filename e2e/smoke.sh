#!/usr/bin/env bash
# End-to-end smoke test for the bigquery-emulator container image.
#
# Build the image from the top-level Dockerfile, verify that the
# binary's --version flag works inside the container, start the
# server, poll for readiness, run `SELECT 1` against the BigQuery
# REST API, and tear down. Intended for ad-hoc local verification
# and as the manual checklist before tagging a release.
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

PROJECT="test"
HTTP="http://localhost:9050"

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

log "waiting for ${HTTP} (timeout=${READY_TIMEOUT}s)"
ready=0
for _ in $(seq 1 "${READY_TIMEOUT}"); do
  # Any HTTP response means the server is listening on the port.
  if curl -s -o /dev/null -m 1 "${HTTP}/"; then
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

log "running SELECT 1 against /projects/${PROJECT}/queries"
response="$(
  curl -s -m 10 \
    -X POST \
    -H 'Content-Type: application/json' \
    -d '{"query":"SELECT 1 AS one","useLegacySql":false}' \
    "${HTTP}/projects/${PROJECT}/queries"
)"
echo "${response}"

# The synchronous query response shape is documented at
# https://cloud.google.com/bigquery/docs/reference/rest/v2/jobs/query;
# rows[0].f[0].v carries the column value as a JSON string. Asserting
# on the shape + value gives a real end-to-end signal (analyzer +
# executor + serializer all on the happy path) rather than just
# "the port is listening".
if ! echo "${response}" | grep -q '"v"[[:space:]]*:[[:space:]]*"1"'; then
  log "FAIL: SELECT 1 did not return a row with value \"1\""
  log "recent emulator logs:"
  docker compose logs --tail=50 bigquery-emulator >&2 || true
  exit 1
fi

log "SELECT 1 returned the expected row"
log "SMOKE TEST PASSED"
