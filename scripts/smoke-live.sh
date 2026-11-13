#!/usr/bin/env bash
# Alcove live smoke tests: the runtime checks unit tests cannot cover.
#
# Run this ON THE DOCKER HOST (for example the Unraid terminal), as root, with the image you
# intend to ship already built or pulled. It uses THROWAWAY containers (name `alcove-smoke`) and a
# scratch directory, and never touches a real `alcove` container, its data or its key.
#
#   ./scripts/smoke-live.sh                              # uses image alcove:dev
#   IMAGE=ghcr.io/herbiewalker/alcove:stable ./scripts/smoke-live.sh
#
# Checks (M2a, M2b):
#   1. keygen makes a key file deliberately, keycheck proves it, and keygen never overwrites it
#   2. no key: refuses to start and says how to fix it; nothing is created
#   3. a key inside the data directory: refuses to start
#   4. a plain key starts UNATTENDED, as non-root, read-only, no capabilities; /healthz says
#      unlocked; docker's healthcheck is healthy
#   5. docker stop shuts down gracefully, and the data volume holds ciphertext only
#   6. a restart with the same key needs no human step
#   7. a different key is refused cleanly and leaves the sealed data untouched
#   8. first run goes to /setup, the setup code is in the log, strict CSP, CSRF refusal, and the
#      allowed-networks gate refuses a public caller and serves a LAN one (M2b)
#
# Checks that arrive with their milestone (each prints SKIP until then, never a fake PASS):
#   - M3:  a large upload from a phone-sized client is accepted, an oversize one gets a fast 413,
#          and a change made just before `docker kill` is still there after the restart
#
# Exit code = number of failed checks (0 = all passed).

set -u

IMAGE="${IMAGE:-alcove:dev}"
PORT="${PORT:-18260}"
NAME="alcove-smoke"
DIR="${SMOKE_DIR:-/mnt/user/appdata/alcove-smoke}"

fails=0
pass() { echo "  PASS  $*"; }
fail() { echo "  FAIL  $*"; fails=$((fails + 1)); }
skip() { echo "  SKIP  $*"; }
say()  { echo; echo "== $* =="; }

# Never remove anything outside a directory that is plainly ours.
case "$(basename "$DIR")" in
  alcove-smoke*) ;;
  *) echo "refusing: SMOKE_DIR must be named alcove-smoke*"; exit 99 ;;
esac

FLAGS=(--read-only --cap-drop ALL --security-opt no-new-privileges:true --tmpfs /tmp:mode=1777,noexec,nosuid,nodev)

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1; rm -rf "$DIR"; }
trap cleanup EXIT
cleanup
mkdir -p "$DIR/keys" "$DIR/keys2" "$DIR/data"
chmod 777 "$DIR/keys" "$DIR/keys2" "$DIR/data"

wait_health() {
  local i
  for i in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:${PORT}/healthz" 2>/dev/null; then echo; return 0; fi
    sleep 1
  done
  return 1
}
start() {
  docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:8080" "${FLAGS[@]}" \
    -v "$1:/run/keys/alcove.key:ro" -v "$DIR/data:/data" "$IMAGE" >/dev/null
}

say "1. keygen, keycheck, and never overwriting a key"
if docker run --rm "${FLAGS[@]}" -v "$DIR/keys:/keys" "$IMAGE" keygen -out /keys/alcove.key >/dev/null 2>&1; then
  pass "keygen wrote a key file"
else fail "keygen failed"; fi
docker run --rm "${FLAGS[@]}" -v "$DIR/keys2:/keys" "$IMAGE" keygen -out /keys/other.key >/dev/null 2>&1
out=$(docker run --rm "${FLAGS[@]}" -v "$DIR/keys:/keys:ro" "$IMAGE" keycheck -file /keys/alcove.key 2>&1)
echo "$out" | grep -q "fingerprint" && pass "keycheck opened it: $out" || fail "keycheck: $out"
if docker run --rm "${FLAGS[@]}" -v "$DIR/keys:/keys" "$IMAGE" keygen -out /keys/alcove.key >/dev/null 2>&1; then
  fail "keygen overwrote an existing key"
else pass "keygen refused to overwrite an existing key"; fi

say "2. no key: refuses to start, says how to fix it, creates nothing"
out=$(docker run --rm "${FLAGS[@]}" -v "$DIR/data:/data" "$IMAGE" 2>&1); rc=$?
if [ $rc -ne 0 ] && echo "$out" | grep -q "alcove keygen" && [ -z "$(ls -A "$DIR/data")" ]; then
  pass "refused with a keygen hint and left the data directory empty"
else fail "no-key start: rc=$rc out=$out"; fi

say "3. a key inside the data directory: refuses to start"
cp "$DIR/keys/alcove.key" "$DIR/data/alcove.key"
out=$(docker run --rm "${FLAGS[@]}" -v "$DIR/data:/data" -e ALCOVE_KEY_FILE=/data/alcove.key "$IMAGE" 2>&1); rc=$?
rm -f "$DIR/data/alcove.key"
if [ $rc -ne 0 ] && echo "$out" | grep -q "inside"; then pass "refused: the key is inside the data directory"
else fail "key-inside-data start: rc=$rc out=$out"; fi

say "4. a plain key starts unattended: non-root, read-only, healthy"
start "$DIR/keys/alcove.key"
body=$(wait_health) || { fail "no /healthz"; docker logs "$NAME" 2>&1 | tail -5; }
echo "$body" | grep -q '"state":"unlocked"' && pass "unlocked with no human step: $body" || fail "state: $body"
user="$(docker inspect --format '{{.Config.User}}' "$NAME")"
case "$user" in ""|root|0|0:0) fail "container user is '$user'" ;; *) pass "user is $user" ;; esac
[ "$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$NAME")" = "true" ] && pass "root filesystem is read-only" || fail "root filesystem is writable"
status=""
for _ in $(seq 1 40); do
  status="$(docker inspect --format '{{.State.Health.Status}}' "$NAME" 2>/dev/null)"
  [ "$status" = "healthy" ] && break
  sleep 2
done
[ "$status" = "healthy" ] && pass "docker healthcheck is healthy" || fail "healthcheck status '${status:-none}'"

say "5. docker stop is graceful and the data volume is ciphertext only"
docker stop -t 15 "$NAME" >/dev/null
docker logs "$NAME" 2>&1 | grep -q 'shutdown complete' && pass "graceful shutdown logged" || fail "no 'shutdown complete'"
docker rm "$NAME" >/dev/null
if [ -f "$DIR/data/manifest.json" ] && [ -f "$DIR/data/alcove.sealed" ]; then pass "sealed store and manifest are on the volume"
else fail "no sealed store: $(ls -A "$DIR/data")"; fi
if grep -rl "SQLite format 3" "$DIR/data" >/dev/null 2>&1; then fail "a plaintext database is on the data volume"
else pass "no plaintext database on the data volume"; fi

say "6. restart with the same key needs no human step"
start "$DIR/keys/alcove.key"
body=$(wait_health) && echo "$body" | grep -q '"state":"unlocked"' && pass "unlocked again after a restart" || fail "restart: $body"
docker stop -t 15 "$NAME" >/dev/null; docker rm "$NAME" >/dev/null

say "7. a different key is refused cleanly and changes nothing"
before=$(sha256sum "$DIR/data/alcove.sealed" | cut -d' ' -f1)
out=$(docker run --rm "${FLAGS[@]}" -v "$DIR/keys2/other.key:/run/keys/alcove.key:ro" -v "$DIR/data:/data" "$IMAGE" 2>&1); rc=$?
after=$(sha256sum "$DIR/data/alcove.sealed" | cut -d' ' -f1)
if [ $rc -ne 0 ] && echo "$out" | grep -qi "unseal failed\|wrong key"; then pass "refused: wrong key"; else fail "wrong-key start: rc=$rc out=$out"; fi
[ "$before" = "$after" ] && pass "the sealed data was not touched" || fail "a wrong key changed the sealed data"

say "8. first run, headers and the network gate (M2b, N17)"
docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:8080" "${FLAGS[@]}" -e ALCOVE_TRUSTED_PROXIES=172.16.0.0/12 \
  -v "$DIR/keys/alcove.key:/run/keys/alcove.key:ro" -v "$DIR/data:/data" "$IMG" >/dev/null
wait_health >/dev/null || fail "no /healthz for the M2b checks"
code=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://127.0.0.1:${PORT}/")
case "$code" in "303 "*"/setup") pass "no accounts yet: / goes to /setup" ;; *) fail "first run did not redirect to /setup: $code" ;; esac
docker logs "$NAME" 2>&1 | grep -q 'setup_code' && pass "the one-time setup code is in the container log" || fail "no setup code in the log"
curl -fsS "http://127.0.0.1:${PORT}/setup" | grep -q 'consenting adults' && pass "setup page states adults only, invite only" || fail "no adults-only notice"
curl -sI "http://127.0.0.1:${PORT}/setup" | grep -qi "^content-security-policy:.*default-src 'none'" && pass "strict Content-Security-Policy" || fail "CSP missing or loose"
pc=$(curl -s -o /dev/null -w '%{http_code}' -X POST -d 'username=intruder&password=intruder-password' "http://127.0.0.1:${PORT}/setup")
[ "$pc" = "403" ] && pass "a form without a CSRF token is refused" || fail "CSRF: got $pc"
pub=$(curl -s -o /dev/null -w '%{http_code}' -H 'X-Forwarded-For: 203.0.113.9' "http://127.0.0.1:${PORT}/static/app.css")
lan=$(curl -s -o /dev/null -w '%{http_code}' -H 'X-Forwarded-For: 192.168.1.5' "http://127.0.0.1:${PORT}/static/app.css")
[ "$pub" = "403" ] && [ "$lan" = "200" ] && pass "a public caller is refused, a LAN caller is served" || fail "network gate: public=$pub lan=$lan"
docker stop -t 15 "$NAME" >/dev/null; docker rm "$NAME" >/dev/null

say "later milestones"
skip "M3: large upload accepted, oversize upload gets a fast 413"
skip "M3: a change made just before docker kill survives the restart"

echo
if [ "$fails" -eq 0 ]; then echo "ALL SMOKE CHECKS PASSED"; else echo "$fails SMOKE CHECK(S) FAILED"; fi
exit "$fails"
