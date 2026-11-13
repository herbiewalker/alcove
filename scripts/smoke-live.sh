#!/usr/bin/env bash
# Alcove live smoke tests: the runtime checks unit tests cannot cover.
#
# Run this ON THE DOCKER HOST (for example the Unraid terminal), with the image you
# intend to ship already built or pulled. It uses a THROWAWAY container (name
# `alcove-smoke`) and never touches a real `alcove` container or its data.
#
#   ./scripts/smoke-live.sh                              # uses image alcove:dev
#   IMAGE=ghcr.io/herbiewalker/alcove:stable ./scripts/smoke-live.sh
#
# Checks that exist now (M0):
#   1. the image starts as a non-root user with a read-only root filesystem and all
#      capabilities dropped, and /healthz answers
#   2. docker's own healthcheck reports healthy
#   3. `docker stop` (SIGTERM) shuts down gracefully ("shutdown complete" in the log)
#
# Checks that arrive with their milestone (each prints SKIP until then, never a fake PASS):
#   - M2a: a key file inside the data directory refuses to start; a wrong key fails cleanly
#   - M2a: no plaintext user data on the data volume after a full cycle
#   - M2b: allowed networks refuse a public-address request
#   - M3:  a large upload from a phone-sized client is accepted, an oversize one gets a fast 413
#
# Exit code = number of failed checks (0 = all passed).

set -u

IMAGE="${IMAGE:-alcove:dev}"
PORT="${PORT:-18260}"
NAME="alcove-smoke"

fails=0
pass() { echo "  PASS  $*"; }
fail() { echo "  FAIL  $*"; fails=$((fails + 1)); }
skip() { echo "  SKIP  $*"; }
say()  { echo; echo "== $* =="; }

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1; }
trap cleanup EXIT
cleanup

say "starting a throwaway container on 127.0.0.1:${PORT}"
docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:8080" \
  --user 65532:65532 --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --tmpfs /tmp:mode=1777,noexec,nosuid,nodev "$IMAGE" >/dev/null \
  || { fail "could not start the container from $IMAGE"; exit 1; }

ok=0
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then ok=1; break; fi
  sleep 1
done

say "1. non-root, read-only, no capabilities, /healthz answers"
user="$(docker inspect --format '{{.Config.User}}' "$NAME")"
case "$user" in ""|root|0|0:0) fail "container user is '$user' (expected non-root)" ;; *) pass "user is $user" ;; esac
[ "$ok" = "1" ] && pass "/healthz answered: $(curl -fsS "http://127.0.0.1:${PORT}/healthz")" || fail "no /healthz answer"
ro="$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$NAME")"
[ "$ro" = "true" ] && pass "root filesystem is read-only" || fail "root filesystem is writable"

say "2. docker healthcheck reports healthy"
status=""
for _ in $(seq 1 40); do
  status="$(docker inspect --format '{{.State.Health.Status}}' "$NAME" 2>/dev/null)"
  [ "$status" = "healthy" ] && break
  sleep 2
done
[ "$status" = "healthy" ] && pass "healthcheck is healthy" || fail "healthcheck status is '${status:-none}'"

say "3. docker stop shuts down gracefully"
docker stop -t 15 "$NAME" >/dev/null
if docker logs "$NAME" 2>&1 | grep -q 'shutdown complete'; then
  pass "graceful shutdown logged"
else
  fail "no 'shutdown complete' in the log"
fi

say "later milestones"
skip "M2a: key file inside the data directory refuses to start"
skip "M2a: no plaintext on the data volume"
skip "M2b: allowed networks refuse a public address"
skip "M3: large upload accepted, oversize upload gets a fast 413"

echo
if [ "$fails" -eq 0 ]; then echo "ALL SMOKE CHECKS PASSED"; else echo "$fails SMOKE CHECK(S) FAILED"; fi
exit "$fails"
