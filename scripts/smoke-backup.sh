#!/usr/bin/env bash
# Alcove end-to-end smoke test (D-28, D-29, D-30): drives a REAL container through first-run setup,
# an upload and its refusals, a backup, a verification and a restore drill, adding a person with a
# temporary password, the forced password change, delete, restore and download, disabling the person,
# and the break-glass `reset-password` (refused while the server runs, working when it is stopped).
# Then it restores the backup with the command-line tools, proves a key with `drill`, and proves a
# damaged archive is refused.
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
PATPW="pat chose this password"
FLAGS=(--read-only --cap-drop ALL --security-opt no-new-privileges:true --tmpfs /tmp:mode=1777,noexec,nosuid,nodev)
URL="http://127.0.0.1:${PORT}"
JAR="$WORK/jar"
J2="$WORK/jar2"

die() { echo "::error::$*"; docker logs "$NAME" 2>&1 | tail -15 || true; exit 1; }
ok()  { echo "  PASS  $*"; }

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

mkdir -p "$WORK/keys" "$WORK/data" "$WORK/backups" "$WORK/restored"
chmod 777 "$WORK/keys" "$WORK/data" "$WORK/backups" "$WORK/restored"

MOUNTS=(-v "$WORK/keys/alcove.key:/run/keys/alcove.key:ro" -v "$WORK/data:/data")

start_server() {
  docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:8080" "${FLAGS[@]}" -e ALCOVE_MAX_UPLOAD_MB=1 \
    "${MOUNTS[@]}" -v "$WORK/backups:/backups" "$IMG" >/dev/null
  for _ in $(seq 1 30); do curl -fsS "$URL/healthz" >/dev/null 2>&1 && break; sleep 1; done
  curl -fsS "$URL/healthz" >/dev/null 2>&1 || die "the server did not start"
}

docker run --rm "${FLAGS[@]}" -v "$WORK/keys:/keys" "$IMG" keygen -out /keys/alcove.key >/dev/null
start_server

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
up() { # up <password> <file> : prints the HTTP status (uses the cookie jar in $JAR)
  local t; t=$(token /uploads)
  curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -b "$JAR" -F "csrf=$t" -F "password=$1" -F remember=1 -F "file=@$2" "$URL/uploads"
}
stepup() { post "/stepup?next=$1" /stepup -d "password=$2" -d "next=$1"; }

# 1. First run: the setup code is in the log; create the administrator.
code=$(docker logs "$NAME" 2>&1 | sed -n 's/.*"setup_code":"\([^"]*\)".*/\1/p' | head -1)
[ -n "$code" ] || die "no setup code in the log"
[ "$(post /setup /setup -d "code=$code" -d username=owner -d "password=$PW" -d "confirm=$PW")" = "303" ] || die "first-run setup failed"
ok "first run created the administrator"

# 1b. Upload a real (synthetic) Tryst backup through the real server, and prove the refusals.
BKPW="synthetic smoke password"
mkdir -p "$WORK/files"; chmod 777 "$WORK/files"
docker run --rm "${FLAGS[@]}" -v "$WORK/files:/files" "$IMG" test-backup -out /files/t.tryst -entries 9 -photos 2 -password "$BKPW" >/dev/null || die "test-backup failed"
docker run --rm "${FLAGS[@]}" -v "$WORK/files:/files" "$IMG" test-backup -out /files/p.tryst -seed 2 -entries 4 -password "pat backup password" >/dev/null || die "test-backup (pat) failed"
$SUDO chmod a+r "$WORK/files/t.tryst" "$WORK/files/p.tryst"
head -c 3145728 /dev/urandom > "$WORK/files/big.bin"
echo "this is a plain text file and not a backup at all" > "$WORK/files/notes.txt"
[ "$(up "wrong password" "$WORK/files/t.tryst")" = "401" ] || die "a wrong backup password was not refused"
[ "$(up "$BKPW" "$WORK/files/notes.txt")" = "422" ] || die "a text file was not refused as not-a-backup"
[ "$(up "$BKPW" "$WORK/files/big.bin")" = "413" ] || die "an oversize upload was not refused with 413"
[ "$(up "$BKPW" "$WORK/files/t.tryst")" = "303" ] || die "a valid backup was refused"
[ "$(up "$BKPW" "$WORK/files/t.tryst")" = "409" ] || die "a duplicate upload was not refused"
curl -fsS -c "$JAR" -b "$JAR" "$URL/uploads" | grep -q "t.tryst" || die "the uploaded backup is not listed"
curl -fsS -c "$JAR" -b "$JAR" "$URL/" | grep -q "<b>9</b> entries" || die "home does not show the 9 uploaded entries"
ok "uploaded a synthetic backup from the real server; wrong password, non-backup, oversize and duplicate were all refused"

# 2. Step-up, then back up from the admin page.
[ "$(stepup /admin/backup "$PW")" = "303" ] || die "step-up failed"
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
if $SUDO grep -q "Synthetic entry" "$WORK/backups/$archive"; then die "uploaded entry text found in the clear inside the backup"; fi
ok "no plaintext database inside the backup"

# 4b. A person: temporary password, forced change, own upload, delete, restore, download, disable.
[ "$(stepup /admin/users "$PW")" = "303" ] || die "step-up for People failed"
t=$(token /admin/users)
body=$(curl -s -c "$JAR" -b "$JAR" --data-urlencode "csrf=$t" -d username=pat -d display=Pat -d role=member "$URL/admin/users/create")
temp=$(echo "$body" | sed -n 's/.*<code>\([a-z2-9-]\{19\}\)<\/code>.*/\1/p' | head -1)
[ -n "$temp" ] || die "no temporary password was shown for the new person"
t2=$(JAR="$J2" token /login)
[ "$(curl -s -o /dev/null -w '%{http_code}' -c "$J2" -b "$J2" --data-urlencode "csrf=$t2" -d username=pat --data-urlencode "password=$temp" "$URL/login")" = "303" ] || die "the new person could not sign in with the temporary password"
forced=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -b "$J2" "$URL/")
case "$forced" in "303 "*"/account/password") ;; *) die "a temporary password did not force a change: $forced" ;; esac
[ "$(JAR="$J2" post /account/password /account/password --data-urlencode "current=$temp" --data-urlencode "password=$PATPW" --data-urlencode "confirm=$PATPW")" = "303" ] || die "the person could not change the temporary password"
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$J2" "$URL/")" = "200" ] || die "the person is still blocked after changing the password"
[ "$(JAR="$J2" up "pat backup password" "$WORK/files/p.tryst")" = "303" ] || die "the person's own upload was refused"
[ "$(JAR="$J2" stepup /uploads "$PATPW")" = "303" ] || die "the person's step-up failed"
rev=$(curl -fsS -b "$J2" "$URL/uploads" | sed -n 's/.*name="id" value="\(rv[a-f0-9]*\)".*/\1/p' | head -1)
[ -n "$rev" ] || die "no upload id on the person's page"
[ "$(JAR="$J2" post /uploads /uploads/delete -d "id=$rev")" = "303" ] || die "the person could not delete their upload"
curl -fsS -b "$J2" "$URL/uploads/deleted" | grep -q "p.tryst" || die "the deleted upload is not in Recently deleted"
[ "$(JAR="$J2" post /uploads/deleted /uploads/restore -d "id=$rev")" = "303" ] || die "the person could not restore their upload"
curl -fsS -b "$J2" "$URL/uploads" | grep -q "p.tryst" || die "the restored upload is not listed"
t3=$(JAR="$J2" token /uploads)
[ "$(curl -s -o "$WORK/pat.zip" -w '%{http_code}' -c "$J2" -b "$J2" --data-urlencode "csrf=$t3" "$URL/uploads/download")" = "200" ] || die "the data download failed"
[ "$(head -c 2 "$WORK/pat.zip")" = "PK" ] || die "the download is not a zip"
ok "a new person: temporary password forces a change, then upload, delete, restore and download all work"

[ "$(stepup /admin/users "$PW")" = "303" ] || die "step-up (2) failed"
padid=$(curl -fsS -c "$JAR" -b "$JAR" "$URL/admin/users" | sed -n 's/.*action="\/admin\/users\/disable".*name="id" value="\([a-z0-9]*\)".*/\1/p' | head -1)
[ -n "$padid" ] || die "no disable button for the new person"
[ "$(post /admin/users /admin/users/disable -d "id=$padid")" = "303" ] || die "disabling the person failed"
gone=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -b "$J2" "$URL/")
case "$gone" in "303 "*"/login") ;; *) die "a disabled person kept their session: $gone" ;; esac
curl -fsS -c "$JAR" -b "$JAR" "$URL/admin/log" | grep -q "user.disable" || die "the disable is not in the admin log"
ok "disabling a person ends their session at once and is in the admin log"

# 4e. Spaces: a signed-out person joins on an invite, an outsider sees nothing, a space code asks, the owner decides.
J3="$WORK/jar3"; J4="$WORK/jar4"
loc=$(curl -s -o /dev/null -w '%{redirect_url}' -c "$JAR" -b "$JAR" --data-urlencode "csrf=$(token /spaces)" -d name="Smoke space" "$URL/spaces")
spid="${loc##*/}"
case "$spid" in sp*) ;; *) die "creating a space did not go to its page: $loc" ;; esac
body=$(curl -s -c "$JAR" -b "$JAR" --data-urlencode "csrf=$(token "/spaces/$spid")" -d days=7 -d label=smoke "$URL/spaces/$spid/invite")
icode=$(echo "$body" | sed -n 's/.*<code>\([a-z2-9-]\{19\}\)<\/code>.*/\1/p' | head -1)
[ -n "$icode" ] || die "no invite code was shown"
[ "$(curl -s -o /dev/null -w '%{http_code}' -c "$J3" -b "$J3" --data-urlencode "csrf=$(JAR="$J3" token /join)" -d "code=aaaa-aaaa-aaaa-aaaa" "$URL/join/code")" = "400" ] || die "a wrong invite code was not refused"
curl -s -c "$J3" -b "$J3" --data-urlencode "csrf=$(JAR="$J3" token /join)" --data-urlencode "code=$icode" "$URL/join/code" | grep -q "What joining shares" || die "the join screen did not show what is shared"
accept=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -c "$J3" -b "$J3" --data-urlencode "csrf=$(JAR="$J3" token /join)" --data-urlencode "code=$icode" -d username=quin --data-urlencode "password=quin chose this one" --data-urlencode "confirm=quin chose this one" -d consent=1 "$URL/join/accept")
case "$accept" in "303 "*"/spaces/$spid") ;; *) die "redeeming the invite failed: $accept" ;; esac
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$J3" "$URL/spaces/$spid")" = "200" ] || die "the new member cannot open the space"
reuse=$(curl -s -o /dev/null -w '%{http_code}' -c "$J4" -b "$J4" --data-urlencode "csrf=$(JAR="$J4" token /join)" --data-urlencode "code=$icode" -d username=again --data-urlencode "password=again chose this" --data-urlencode "confirm=again chose this" -d consent=1 "$URL/join/accept")
[ "$reuse" = "400" ] || die "a spent invite was accepted again: $reuse"
curl -fsS -c "$JAR" -b "$JAR" "$URL/spaces/$spid" | grep -q "@quin" || die "the owner does not see the new member"
ok "a signed-out person joined a space with a one-time invite; a spent invite was refused"

# An outsider (made with a server invite) cannot see the space or tell it exists; a space code only lets them ask.
[ "$(stepup /admin/users "$PW")" = "303" ] || die "step-up (3) failed"
t=$(token /admin/users)
scode=$(curl -s -c "$JAR" -b "$JAR" --data-urlencode "csrf=$t" -d days=7 "$URL/admin/invites/create" | sed -n 's/.*<code>\([a-z2-9-]\{19\}\)<\/code>.*/\1/p' | head -1)
[ -n "$scode" ] || die "no server invite code was shown"
J5="$WORK/jar5"
curl -s -o /dev/null -c "$J5" -b "$J5" --data-urlencode "csrf=$(JAR="$J5" token /join)" --data-urlencode "code=$scode" "$URL/join/code"
[ "$(curl -s -o /dev/null -w '%{http_code}' -c "$J5" -b "$J5" --data-urlencode "csrf=$(JAR="$J5" token /join)" --data-urlencode "code=$scode" -d username=ona --data-urlencode "password=ona chose this one" --data-urlencode "confirm=ona chose this one" "$URL/join/accept")" = "303" ] || die "the server invite did not create an account"
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$J5" "$URL/spaces/$spid")" = "404" ] || die "an outsider can open the space"
curl -fsS -b "$J5" "$URL/spaces" | grep -q "Smoke space" && die "an outsider's list shows the space"
spcode=$(curl -s -c "$JAR" -b "$JAR" --data-urlencode "csrf=$(token "/spaces/$spid")" -d do=rotate "$URL/spaces/$spid/code" | sed -n 's/.*<code>\([a-z2-9-]\{19\}\)<\/code>.*/\1/p' | head -1)
[ -n "$spcode" ] || die "no space code was shown"
curl -s -o /dev/null -c "$J5" -b "$J5" --data-urlencode "csrf=$(JAR="$J5" token /join)" --data-urlencode "code=$spcode" "$URL/join/code"
ask=$(curl -s -o /dev/null -w '%{http_code}' -c "$J5" -b "$J5" --data-urlencode "csrf=$(JAR="$J5" token /join)" --data-urlencode "code=$spcode" -d consent=1 "$URL/join/accept")
[ "$ask" = "303" ] || die "asking to join with a space code failed: $ask"
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$J5" "$URL/spaces/$spid")" = "404" ] || die "asking to join let someone in"
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$J5" "$URL/spaces/$spid/entries")" = "404" ] || die "asking to join showed the shared entries"
onaid=$(curl -fsS -c "$JAR" -b "$JAR" "$URL/spaces/$spid" | sed -n 's/.*action="\/spaces\/[a-z0-9]*\/approve".*name="user" value="\([a-z0-9]*\)".*/\1/p' | head -1)
[ -n "$onaid" ] || die "the owner does not see the request"
[ "$(post "/spaces/$spid" "/spaces/$spid/approve" -d "user=$onaid")" = "303" ] || die "approving failed"
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$J5" "$URL/spaces/$spid")" = "200" ] || die "an approved person cannot open the space"
# 4f. Shared entries: a member sees the owner's entries, a private entry is hidden, leaving closes it.
rows() { curl -fsS -b "$1" "$URL/spaces/$spid/entries" | grep -c '<tr><td>' || true; }
n=$(rows "$J3")
[ "$n" -gt 0 ] || die "a member sees none of the owner's entries in the space"
curl -fsS -b "$J3" "$URL/spaces/$spid/entries" | grep -q "Synthetic entry" || die "the shared list does not show the owner's notes"
eid=$(curl -fsS -c "$JAR" -b "$JAR" "$URL/entries" | sed -n 's/.*name="id" value="\([^"]*\)".*/\1/p' | head -1)
[ -n "$eid" ] || die "no entry id on My entries"
[ "$(post /entries /entries/private -d "id=$eid" -d private=1)" = "303" ] || die "could not mark an entry private"
m=$(rows "$J3")
[ "$m" -eq $((n - 1)) ] || die "a private entry is still shown to the member ($n then $m)"
[ "$(post /entries /entries/private -d "id=nope" -d private=1)" = "404" ] || die "an unknown entry id was accepted"
[ "$(post /entries /entries/private -d "id=$eid" -d private=0)" = "303" ] || die "could not share the entry again"
ok "members see each other's entries; a private entry is hidden and can be shared again"

[ "$(JAR="$J3" post "/spaces/$spid" "/spaces/$spid/leave")" = "303" ] || die "a member could not leave"
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$J3" "$URL/spaces/$spid")" = "404" ] || die "a member who left can still open the space"
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$J3" "$URL/spaces/$spid/entries")" = "404" ] || die "a member who left can still read the shared entries"
ok "an outsider sees nothing; a space code only asks; the owner approves; leaving closes the door at once"

# 4c. Break-glass: refused while the server runs.
out=$(docker run --rm "${FLAGS[@]}" "${MOUNTS[@]}" "$IMG" reset-password -user owner -generate 2>&1) && die "reset-password ran while the server was running"
echo "$out" | grep -q "Stop it first" || die "the refusal did not say to stop the server: $out"
ok "reset-password is refused while the server holds the data folder"

docker stop -t 15 "$NAME" >/dev/null
docker rm "$NAME" >/dev/null

# 4d. ...and works when the server is stopped; the next sign-in must change the password.
out=$(docker run --rm "${FLAGS[@]}" "${MOUNTS[@]}" "$IMG" reset-password -user owner -generate) || die "reset-password failed with the server stopped"
newtemp=$(echo "$out" | sed -n 's/^temporary password (shown only now): \(.*\)$/\1/p')
[ -n "$newtemp" ] || die "no temporary password from reset-password: $out"
start_server
rm -f "$JAR"
t4=$(token /login)
[ "$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -b "$JAR" --data-urlencode "csrf=$t4" -d username=owner --data-urlencode "password=$PW" "$URL/login")" = "401" ] || die "the old administrator password still works after a reset"
[ "$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -b "$JAR" --data-urlencode "csrf=$t4" -d username=owner --data-urlencode "password=$newtemp" "$URL/login")" = "303" ] || die "the reset password does not sign in"
case "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -b "$JAR" "$URL/")" in "303 "*"/account/password") ;; *) die "a reset did not force a password change" ;; esac
ok "reset-password works with the server stopped, forces a change, and survives a restart"
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
echo "$out" | grep -q "uploads read back: 1" || die "the drill did not read the uploaded backup back: $out"
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
