#!/usr/bin/env bash
# Alcove backup smoke test (D-28): drives a REAL container through first-run setup, a backup, a
# verification and a restore drill from the admin pages, then restores the backup with the
# command-line tools, proves a key with `drill`, and proves a damaged archive is refused.
#
# Used by CI (image job) and scripts/smoke-live.sh. It uses a throwaway container and a scratch
# directory, never a real `alcove` container, data or key.
#
#   IMG=alcove:ci WORK=$(mktemp -d) SUDO=sudo ./scripts/smoke-backup.sh
#
# SUDO is needed on a CI runner because the container writes files as its own user (mode 0600).
# Exit code 0 = all passed.

set -eu

IMG="${IMG:?set IMG to the image to test}"
WORK="${WORK:?set WORK to a scratch directory}"
PORT="${PORT:-18082}"
SUDO="${SUDO:-}"
NAME="alcove-smoke-bk"
PW="smoke test passphrase 1"
FLAGS=(--read-only --cap-drop ALL --security-opt no-new-privileges:true --tmpfs /tmp:mode=1777,noexec,nosuid,nodev)
URL="http://127.0.0.1:${PORT}"
JAR="$WORK/jar"

die() { echo "::error::$*"; docker logs "$NAME" 2>&1 | tail -15 || true; exit 1; }
ok()  { echo "  PASS  $*"; }

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

mkdir -p "$WORK/keys" "$WORK/data" "$WORK/backups" "$WORK/restored"
chmod 777 "$WORK/keys" "$WORK/data" "$WORK/backups" "$WORK/restored"

docker run --rm "${FLAGS[@]}" -v "$WORK/keys:/keys" "$IMG" keygen -out /keys/alcove.key >/dev/null
docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:8080" "${FLAGS[@]}" \
  -v "$WORK/keys/alcove.key:/run/keys/alcove.key:ro" -v "$WORK/data:/data" -v "$WORK/backups:/backups" "$IMG" >/dev/null
for _ in $(seq 1 30); do curl -fsS "$URL/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "$URL/healthz" >/dev/null 2>&1 || die "the server did not start"

token() { curl -fsS -c "$JAR" -b "$JAR" "$URL$1" | sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' | head -1; }
post()  { # post <token page> <path> [curl -d args...]; prints the HTTP status
  local page="$1" path="$2"; shift 2
  local t; t=$(token "$page")
  curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -b "$JAR" --data-urlencode "csrf=$t" "$@" "$URL$path"
}
wait_for() { # wait_for <text> : poll the backup page until the job finishes
  local want="$1" body=""
  for _ in $(seq 1 120); do
    body=$(curl -fsS -c "$JAR" -b "$JAR" "$URL/admin/backup")
    case "$body" in *"This page refreshes by itself"*) sleep 1; continue ;; esac
    case "$body" in *"$want"*) return 0 ;; esac
    echo "$body" | sed -n 's/.*class="notice \(ok\|bad\)"[^>]*>\([^<]*\)<.*/\1: \2/p' | head -3
    return 1
  done
  return 1
}

# 1. First run: the setup code is in the log; create the administrator.
code=$(docker logs "$NAME" 2>&1 | sed -n 's/.*"setup_code":"\([^"]*\)".*/\1/p' | head -1)
[ -n "$code" ] || die "no setup code in the log"
[ "$(post /setup /setup -d "code=$code" -d username=owner -d "password=$PW" -d "confirm=$PW")" = "303" ] || die "first-run setup failed"
ok "first run created the administrator"

# 2. Step-up, then back up from the admin page.
[ "$(post "/stepup?next=/admin/backup" /stepup -d "password=$PW" -d next=/admin/backup)" = "303" ] || die "step-up failed"
[ "$(post /admin/backup /admin/backup/run)" = "303" ] || die "could not start a backup"
wait_for "written and verified" || die "the backup did not finish cleanly"
archive=$(basename "$(ls "$WORK"/backups/alcove-backup-*.tar | head -1)")
[ -n "$archive" ] && [ -f "$WORK/backups/$archive.sha256" ] || die "no archive or checksum file"
ok "backup written and verified in the container: $archive"

# 3. Verify and drill from the admin page.
[ "$(post /admin/backup /admin/backup/verify -d "name=$archive")" = "303" ] || die "verify did not start"
wait_for "every checksum matches" || die "verify from the page failed"
[ "$(post /admin/backup /admin/backup/drill -d "name=$archive")" = "303" ] || die "drill did not start"
wait_for "Restore drill passed" || die "the restore drill from the page failed"
ok "verify and restore drill passed from the admin page"

# 4. The archive holds ciphertext only.
if $SUDO grep -q "SQLite format 3" "$WORK/backups/$archive"; then die "plaintext SQLite header inside the backup"; fi
ok "no plaintext database inside the backup"

docker stop -t 15 "$NAME" >/dev/null
docker rm "$NAME" >/dev/null

# 5. Command-line tools work with the server stopped.
docker run --rm "${FLAGS[@]}" -v "$WORK/backups:/backups:ro" "$IMG" backup-verify -file "/backups/$archive" | grep -q "every checksum matches" || die "backup-verify"
docker run --rm "${FLAGS[@]}" -v "$WORK/backups:/backups:ro" -v "$WORK/restored:/restored" "$IMG" restore -backup "/backups/$archive" -to /restored/data | grep -q "restored" || die "restore"
$SUDO test -f "$WORK/restored/data/alcove.sealed" && $SUDO test -f "$WORK/restored/data/manifest.json" || die "restore did not extract the database"
if docker run --rm "${FLAGS[@]}" -v "$WORK/backups:/backups:ro" -v "$WORK/restored:/restored" "$IMG" restore -backup "/backups/$archive" -to /restored/data >/dev/null 2>&1; then
  die "restore overwrote an existing folder"
fi
ok "backup-verify and restore work from the command line; restore refuses a non-empty folder"

# 6. The key's second copy is proved by restoring a real backup with it.
out=$(docker run --rm "${FLAGS[@]}" -v "$WORK/keys:/keys:ro" -v "$WORK/backups:/backups:ro" "$IMG" drill -backup "/backups/$archive" -key /keys/alcove.key -tmp /tmp/drill) || die "drill with the key failed"
echo "$out" | grep -q "chain verifies" || die "drill output: $out"
docker run --rm "${FLAGS[@]}" -v "$WORK/keys:/keys" "$IMG" keygen -out /keys/other.key >/dev/null
if docker run --rm "${FLAGS[@]}" -v "$WORK/keys:/keys:ro" -v "$WORK/backups:/backups:ro" "$IMG" drill -backup "/backups/$archive" -key /keys/other.key -tmp /tmp/drill >/dev/null 2>&1; then
  die "a different key opened the backup"
fi
ok "drill passes with the real key and fails with a different one"

# 7. A damaged archive is refused.
$SUDO cp "$WORK/backups/$archive" "$WORK/bad.tar"
$SUDO chmod 666 "$WORK/bad.tar"
size=$($SUDO wc -c < "$WORK/bad.tar")
printf '\377' | dd of="$WORK/bad.tar" bs=1 seek=$((size / 2)) conv=notrunc 2>/dev/null
mkdir -p "$WORK/bad"; cp "$WORK/bad.tar" "$WORK/bad/alcove-backup-20260101T000000Z-bad.tar"; chmod 777 "$WORK/bad"
if docker run --rm "${FLAGS[@]}" -v "$WORK/bad:/bad:ro" "$IMG" backup-verify -file /bad/alcove-backup-20260101T000000Z-bad.tar >/dev/null 2>&1; then
  die "a damaged backup verified"
fi
ok "a damaged backup is refused"

echo "BACKUP SMOKE PASSED"
