# Alcove — Runbook

> What to do, step by step, for the things an operator does. Commands use `docker run --rm <image>` because
> the image has no shell; replace `<image>` with the one you run (for example `ghcr.io/herbiewalker/alcove:stable`).
> Nothing here needs the internet.

## 1. First start

1. Make the key once, in a folder that is **not** the data folder. The container runs as user 65532 and cannot read the root-only
   Unraid flash drive, so on Unraid use its own folder and put a copy on the flash drive:
   ```
   mkdir -p /mnt/user/appdata/alcove-key /boot/config/alcove
   chown 65532:65532 /mnt/user/appdata/alcove-key
   docker run --rm -v /mnt/user/appdata/alcove-key:/keys <image> keygen -out /keys/alcove.key
   cp /mnt/user/appdata/alcove-key/alcove.key /boot/config/alcove/alcove.key
   ```
   It refuses to overwrite a key. The server never makes a key itself. The flash copy is your first backup of it; the second must be
   somewhere that is not this machine.
2. **Save a second copy of the key somewhere that is not this machine** (a password manager is ideal), then prove the copy
   (section 3). Do this before the first real upload: if every copy of the key is lost, the data cannot be recovered.
3. Set `ALCOVE_DATA_PATH`, `ALCOVE_KEY_PATH` and `ALCOVE_BACKUP_PATH` in `.env`, then `docker compose up -d`.
4. Read the container log for the one-time **setup code** (`setup_code`). Open the server in a browser, enter the code and
   create the administrator. The code works once and only while there are no accounts.

## 2. Backing up

Admin console → **Backups** → **Back up now** (asks for your password again). It writes one archive of encrypted files into
`ALCOVE_BACKUP_PATH`, re-reads it to check every checksum, and removes the oldest beyond `ALCOVE_BACKUP_KEEP` (default 7). The
newest backup is never deleted.

- Do it on purpose, not on a timer: before an upgrade, after a burst of edits, and at least monthly.
- A backup is **ciphertext**. It is safe to copy to another disk, a share or offsite, and it is **useless without the key file**.
- Copy the whole folder, including the `.sha256` files. `docker run --rm -v <folder>:/b:ro <image> backup-verify -file /b/<name>.tar`
  re-checks a copy anywhere, with no key.

## 3. Proving the key copy (the mandatory first check)

Fingerprints can match while a copy is corrupt, so prove the copy by **restoring a real backup with it**:

```
docker run --rm --tmpfs /tmp:mode=1777,size=512m,nosuid,nodev -v <folder with the COPY>:/keys:ro -v <backup folder>:/backups:ro <image> \
    drill -backup /backups/<name>.tar -key /keys/<copy file> -tmp /tmp/drill
```

`--tmpfs /tmp` is needed because the drill refuses to open a database anywhere but memory-backed storage. If the copy is on the Unraid flash drive (root-only), add `--user 0:0` after `--rm`. It restores into a throwaway place, opens the backup with that key, checks the admin-log chain, reads every upload and decrypts
every photo, prints counts only, and deletes what it made. `ok: key ... opens ...` means that copy can recover your data. A
wrapped (passphrase) copy takes the passphrase on the first line of standard input. Record the date in `CRITICAL_ASSETS.md`.

The **Restore drill** button on the Backups page does the same thing with the running server's own key, which proves the backup
but not your copy of the key.

## 4. Restoring for real

1. Stop the container: `docker compose stop`.
2. Move the damaged data folder aside (do not delete it yet).
3. Extract the backup into a new, empty folder (no key needed, files stay encrypted):
   ```
   docker run --rm -v <backup folder>:/b:ro -v <new parent>:/out <image> restore -backup /b/<name>.tar -to /out/data
   ```
   It verifies every checksum first and refuses a folder that already has something in it.
4. Point `ALCOVE_DATA_PATH` at the new folder and `docker compose up -d`. The server needs the **same key file**.
5. Sign in, open the admin log and press **Verify the chain now**. Anything done after the backup was taken is not in it.

## 5. Locked out of the network settings

A console change to the allowed networks reverts by itself unless you confirm it within 60 seconds. If you are locked out some
other way, set `ALCOVE_ALLOWED_NETWORKS` (and `ALCOVE_REQUIRE_TLS=false`) in `.env`, then `docker compose up -d`. A value in the
environment always beats the console.

## 6. Putting HTTPS in front (optional)

The default is a password-protected wifi over plain HTTP (R14). To encrypt the connection, run a reverse proxy that you control on the
same host, set `ALCOVE_TRUSTED_PROXIES` to the address Alcove sees the proxy connect from, set `ALCOVE_PROXY_MODE` to true, and open
Alcove through the proxy. Then, from that HTTPS address, turn on **Require HTTPS** in the console. Never publish Alcove's own port
to the internet, and never let the proxy itself be reachable from outside your network.

**Check before you trust it.** After the proxy is in, open Admin, Settings, and read the line "Your own address is". It must show YOUR device's
address. If it shows the proxy's address, Alcove cannot tell people apart, the allowed-networks rule is weakened and rate limits are
shared: undo it and fix the proxy's forwarded-address header before going on.

- **Caddy** (certificate by DNS challenge, so nothing is opened to the internet; needs a domain you own and a Caddy build with your
  DNS provider's module):
  ```
  alcove.home.example.com {
      tls { dns <provider> <token-from-the-provider> }
      reverse_proxy 127.0.0.1:8260
  }
  ```
  Caddy sends the client's address in `X-Forwarded-For` by itself. Put the token in Caddy's own environment, never in Alcove's.
- **Nginx Proxy Manager:** a proxy host to the Alcove port, "Websockets" off, "Block common exploits" on, a Let's Encrypt certificate by
  DNS challenge, Force SSL on. Do not enable its external access list features for the internet.
- **`tailscale serve`:** `tailscale serve --bg --https=443 http://127.0.0.1:8260` on the host, so only your tailnet reaches it
  (never `tailscale funnel`). Do the "Your own address" check above: if it does not show the device's own address, use Caddy instead.

If you lock yourself out of the network settings, section 5 is the way back.

## 7. Lost the administrator password

There is a break-glass command. It needs the same access as running the server itself (the data folder and the key file), and the
server must be **stopped**, because two processes must never write the same database (the command is refused, and changes nothing, if
the server is running).

1. `docker compose stop`
2. Make a temporary password and set it:
   ```
   docker run --rm -v <key file>:/run/keys/alcove.key:ro -v <data folder>:/data --tmpfs /tmp <image> \
       reset-password -user <name> -generate
   ```
   It prints the temporary password once. (To choose the password yourself, leave out `-generate` and pipe it in on standard
   input; if the key file is passphrase-wrapped, the key passphrase is the first line.)
3. `docker compose start`, sign in with the temporary password, and choose a new one when asked.

The reset ends nothing else (it does not turn off two-step sign-in; see 7a) and is written to the admin log as done by the system. It will not reset a deleted account (restore it
from the People page first), and it does not need any other administrator.

## 7a. Two-step sign-in (administrators): setting it up and getting back in

Optional, and off until you turn it on. With it on, signing in needs your password and a 6-digit code from an authenticator app.

**Set it up.** Account, Two-step sign-in, Set up. Scan the picture with any authenticator app (or type the key under it), then type the
code the app shows. Nothing is turned on until that code is right. Then you get ten recovery codes, shown once: keep them on paper, not
on the phone. Other devices are signed out.

**Check the clock.** A code only works if the server's clock and the phone's clock agree to within about 30 seconds. Admin, Health shows
the server's time in UTC. Set the phone's clock to automatic. A wrong clock is the usual reason a correct-looking code is refused.

**Lost the phone? The way back, in order:**
1. Type a recovery code in the same box at the second step. Each works once. Then make new ones (Account, Two-step sign-in).
2. Another administrator opens People and presses **Reset two-step** on your row (they confirm their password first). You are signed out
   everywhere and can sign in with your password alone, then set it up again.
3. On the server, with Alcove stopped (the same rules as section 7):
   ```
   docker run --rm -v <key file>:/run/keys/alcove.key:ro -v <data folder>:/data --tmpfs /tmp <image> \
       reset-2fa -user <name>
   ```
   It turns two-step off for that person, forgets the secret and recovery codes, and leaves the password alone. If the key file is
   passphrase-wrapped, the key passphrase is the first line on standard input. It is logged as done by the system.

Five wrong codes lock the second step for that account for ten minutes (it needs the password first, so no one outside can use this to
lock you out). The admin log shows `session.2fa.locked`. Wait, then sign in again.

**Drill it once** before you rely on it, with the same spirit as the restore drill: turn it on, sign out, sign in with the app, sign out,
sign in with one recovery code, make new recovery codes, then run step 3 on a test copy or practise step 2 with a second administrator.
A way back you have never used is a hope. Admin, Health warns when an administrator is down to two recovery codes.

## 8. Uploading a backup from a phone

1. In Tryst on the phone, export a backup and note the password you chose.
2. On the same wifi, open Alcove in the phone browser, sign in, go to **My data**, choose the file, enter the password and press
   Upload. A bar shows progress, then "Checking the backup".
3. A wrong password, a file that is not a Tryst backup, a damaged file and a file already uploaded are all refused with a plain
   message, and nothing is saved. A good upload becomes your current data; older uploads are kept as history.
4. The limit is `ALCOVE_MAX_UPLOAD_MB` (default 512). A slow wifi is fine: that one request is allowed to run for up to 25 minutes.

Going the other way (edits back onto the phone) is section 14.

To try Alcove with no real data, make a fake backup: `docker run --rm -v <folder>:/f <image> test-backup -out /f/test.tryst`. It prints
the password to use.

## 9. The search index or cache looks wrong

They are copies made from each person's current upload and can be thrown away. Admin console → **Backups** → **Rebuild now** (asks
for your password again). It needs each person's remembered backup password; anyone who chose not to remember it is skipped and
listed in the result. No upload is touched. Edits you made in Alcove are kept: they are replayed from their records on top of the upload.

## 10. Adding and removing people

Admin console → **People**.

- **Add a person:** pick a username and a role. A temporary password is made and **shown once**; give it to them in person. At their
  first sign-in they must choose their own password before anything else works. Reset a forgotten password the same way.
- **Disable** ends their sessions at once and blocks sign-in; **Enable** undoes it. Nothing is deleted.
- **Delete** (the account must be disabled first) starts a grace period of `ALCOVE_ADMIN_RECOVERY_DAYS` (45). **Restore** during it
  brings the account back, still disabled. After it, a purge that runs by itself every hour removes the account and everything it
  owns for good: the stored files, the rows, the search index, the remembered backup password and the data key.
- Everyone asks for their password again before the sensitive steps here. You cannot disable, delete or reset yourself, so the
  server is never left without an administrator.
- A person can download everything they have uploaded (My data → Download all my data) and can restore their own deleted uploads
  for `ALCOVE_USER_RECOVERY_DAYS` (10); you can restore any of them for the longer window (Recently deleted).
- Older instance backups still contain a purged person's data until they age out of retention (R15, R19). Prune old backups when
  somebody has asked to be removed.

## 11. Spaces and invites

- **Start a space:** Spaces → Start a space. You become the owner. Nobody else sees it, or can tell it exists.
- **Invite someone** (owner): on the space page, "Make an invite". The one-time code is shown once. Give it to one person, in person or
  through your own channel. They open the server's **/join** page and enter it. If they have no account they create one there; if they
  do, they just join. It works once, expires (1, 7 or 30 days) and can be cancelled from the same page.
- **Space code** (owner): lets people who already have an account *ask* to join. You see each request and decide. "Make a new code" stops
  the old one; "Turn the code off" stops them all. A code is shown once and cannot be looked up again.
- **Invite someone to the server** (admin): People → "Invite someone to the server". It makes one account and nothing else.
- Everyone sees exactly what joining shares before it happens, and ticks a box. The wording is versioned: if it ever changes in substance,
  everyone is asked again before anything of theirs is shown.
- **Leave** at any time; the **owner** can remove a member, hand the space to another member, or delete it (restorable for the same windows
  as everything else). The only owner cannot leave without handing over or deleting.
- The **administrator** sees every space (Admin → Spaces) and can delete or restore one. Every look is in the admin log.

## 12. The first live deploy on Unraid (L1)

Do these in order. Nothing here needs the internet except pulling the image once.

**A. Before you start.** A release is published and its image package on GHCR is public, so the Docker tab can pull
`ghcr.io/herbiewalker/alcove:stable` and check for updates. You have a folder for the key (and a copy of it on the flash drive) and a share for
instance backups on a **different disk** from the data.

**B. Set it up.**
1. Make the folders and give them to the container's user (the image runs as 65532, not root):
   ```
   mkdir -p /mnt/user/appdata/alcove/data /mnt/user/backups/alcove
   chown -R 65532:65532 /mnt/user/appdata/alcove/data /mnt/user/backups/alcove
   ```
2. Make the key (section 1 step 1, which also puts a copy on the flash drive), then **save another copy somewhere that is not this machine**.
3. Install the template: copy `deploy/unraid-template.xml` to `/boot/config/plugins/dockerMan/templates-user/alcove.xml`, then in the
   Docker tab choose Add Container and pick `alcove` from the template list. Check the three paths and the port, then Apply.
4. Read the log for the setup code: `docker logs alcove 2>&1 | grep setup_code`. Open `http://<server>:8260/` from a computer on the
   LAN, enter the code and make the administrator. Write the administrator password down in the same place as the key copy.
5. Open Admin, Settings and check that "Your own address is" shows your own LAN address. Leave Allowed networks at the default (private ranges).

**C. Prove the safety net before any real data.**
1. Admin, Backups: run **Back up now**, then **Verify** and **Restore drill**. All three must say they passed.
2. Prove your key copy with the command in section 3 (this is the mandatory first check). Record the date in `CRITICAL_ASSETS.md`.
3. Confirm the container is the least-privilege one: `docker inspect alcove` shows user 65532, ReadonlyRootfs true, no added
   capabilities, and one published port.

**D. The validation ladder (principle 11).** Take each rung only when the one before is clean, and run a backup and a drill after each.
1. **Synthetic data.** Make fake backups (`test-backup`, section 8), upload one as you and one as a second test person, make a space,
   invite the second person, and look at the calendar, the list, Compare and the profile cards. Try the private flag, pause and leave.
2. **One real backup.** Upload one real backup from one phone. Check the entry count on the My data page matches what Tryst shows, and
   open the calendar. Nothing in a space is shown until you both agreed to the join text.
3. **The full history, both of you.** Upload both real backups, join one space, and check the calendar and Compare against what you
   know happened. Set each person's time zone on their profile.

**E. From a real phone.** On the same wifi, open Alcove in the phone browser, sign in, upload a backup from the phone (section 8) and open
the calendar. Add the page to the home screen if you like. This is the check no test can do for you.

**F. If something is wrong.** Stop the container (the data is untouched) and read section 4 to restore from an instance backup. The
setup code only works while there are no accounts, so a failed first run can be wiped by stopping the container and removing the
contents of the data folder, then starting again.

**G. Done means:** both of you use it for real, a backup and a restore drill have passed since the last upload, you have restored one edited export on the phone (section 14, after a fresh phone backup), and the README status
line says what is and is not proven.

## 13. Updating to a new release

1. **Back up first** (section 2): Admin, Backups, Back up now, then Verify. This is also your way back.
2. Read the new version's section in `CHANGELOG.md`. It says whether the database changes and whether you have to do anything afterwards.
3. In the Docker tab, update the container (it pulls `:stable`), or set the tag to the version you want. The container restarts and
   migrates the database by itself. Your data and key are untouched.
4. Check the log for errors, open the admin console and its Health page (it lists anything that needs attention), and look at one person's data.

**Going back is not "set the tag back".** Migrations only go forward, and an older build refuses to open a newer database (it says the
schema is newer than it understands) and does not start. To return to an older version, stop the container, restore the instance backup
you took in step 1 (section 4), then start the older image. Anything done after that backup is gone, so do this only if the new version is
really unusable.

Version notes that need a step:

- **0.1.2 to 0.1.3:** the database goes from schema 10 to 11 (two new tables for two-step sign-in). Nothing else to do. Two-step sign-in is off until an administrator turns it on (section 7a); photo thumbnails appear by themselves. Take the instance backup first, as always.
- **0.1.3 to the next release (D-52):** the database goes from schema 11 to 12 (one new table for profile and partner pictures), and the join wording changes to version 3, so **every member agrees again once** (until they do, their entries are hidden from the space and they see nobody else's, as the join screen already says). Pictures appear after each person's next upload or after Rebuild.
- **0.1.1 to 0.1.2:** the database goes from schema 9 to 10 (one new table). Nothing else to do. Editing and export work on data you
  uploaded earlier; if the Export page says your data needs a rebuild, do section 9 once.
- **0.1.0 to 0.1.1:** after updating, press **Rebuild now** once (section 9), so photos from an upload you already have are copied out.

## 14. Editing your data and sending it back to the phone

This is for everyone, not just the administrator. Your uploaded backup is never changed; every change is saved on top of it as a new
version, and nothing reaches your phone until you restore a file there yourself.

**Editing.** My data → My entries. Open an entry and press **Edit** to change the time, length, rating, mood, who started it,
protection, orgasm count, acts, positions, kinks, settings, toys, occasions and the note. **Add an entry** and **Delete this entry**
are on the same pages. **History** lists every change, newest first, with an **Undo** on each; undoing is itself a new entry in the
history. **My partners** renames a partner or edits a note. A deleted entry sits in **Recently deleted entries** and can be restored for
`ALCOVE_USER_RECOVERY_DAYS` (10); the administrator can restore it for the longer window. If someone else (the administrator) changed
something, the history says "The administrator".

- A page opened before a change refuses to save over it ("this was changed after you opened it"). Open it again.
- Edited notes and ratings show in your spaces at once; a private entry stays private; a deleted entry leaves every space.
- **Uploading a newer backup starts over.** Edits made on the old upload stay in its history but are not carried onto the new one,
  because the phone is the truth for what it holds. The My data page warns you before that happens. Export first if you want to keep them.

**Sending the edits to your phone.** My data → **Export to Tryst**. Read the page: it says which upload the file is based on, what
changed since, and what Tryst should report afterwards.

1. On the phone, in Tryst: Settings → Backup & restore → **Export encrypted backup**. Do this first. Restoring replaces what is on the
   phone, so anything you added there after your last upload is lost without it.
2. In Alcove, tick that you did that, choose the password for the file (the same as your backup password, or a new one of at least 8
   characters typed twice; Tryst asks for it when you restore and nobody can recover a lost one), confirm your Alcove password again,
   and press **Download the backup**. If Alcove does not keep your backup password it asks for it once to open your upload.
3. On the phone, open **Settings → Backup & restore → Import backup**, pick the file from Downloads, enter the password, and leave
   **Replace my current data with the backup (recommended)** switched **on**. **Never switch it off:** merging keeps entries you deleted
   and removes the partner links of others (D-15, D-43).
4. Check the count in Tryst's message ("Brought in N trysts, M partners and K photos") against the Export page.

Refusals you may see: your data needs a rebuild (ask the administrator to do section 9), or the backup comes from a newer Tryst than this
server understands (export stays off for it). A file that fails its own check is never offered.

## 15. Editing someone's data as the administrator

Admin console → **People** → **Data** next to a person. You can open their entries and partners, change, undo, delete and restore them,
and see what they deleted (restorable by you for `ALCOVE_ADMIN_RECOVERY_DAYS`, 45). You cannot add an entry for them, see their photos
here, or export their data.

- Every page you open there is written to the admin log, and every change is written in the same transaction as the change. The log
  names the table, the person and row, and the field names, never a value (constraint 8).
- A change asks for your password again (use "Confirm it is you" on the page first).
- The person sees your change in the entry's history as "The administrator" and can undo it. Tell them why you made it.
- Rebuilding (section 9) keeps all edits; they are replayed from their records.
