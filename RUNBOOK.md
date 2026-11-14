# Alcove — Runbook

> What to do, step by step, for the things an operator does. Commands use `docker run --rm <image>` because
> the image has no shell; replace `<image>` with the one you run (for example `ghcr.io/herbiewalker/alcove:stable`).
> Nothing here needs the internet.

## 1. First start

1. Make the key once, on a folder that is **not** the data folder (on Unraid, the flash drive):
   ```
   docker run --rm -v /boot/config/alcove:/keys <image> keygen -out /keys/alcove.key
   ```
   It refuses to overwrite a key. The server never makes a key itself.
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
docker run --rm -v <folder with the COPY>:/keys:ro -v <backup folder>:/backups:ro <image> \
    drill -backup /backups/<name>.tar -key /keys/<copy file> -tmp /tmp/drill
```

It restores into a throwaway place, opens the backup with that key, checks the admin-log chain, reads every upload and decrypts
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

The default is a password-protected wifi over plain HTTP (R14). To encrypt the connection, run a reverse proxy that you control
(Caddy or Nginx Proxy Manager with a DNS-challenge certificate, or `tailscale serve`) on the same host, set
`ALCOVE_TRUSTED_PROXIES` to its address, and open Alcove through the proxy. Then, from that HTTPS address, turn on **Require
HTTPS** in the console. Never publish Alcove's own port to the internet.

## 7. Lost the administrator password

There is no reset yet. Until a break-glass command exists, a lost admin password means starting from an empty store (restore an
older backup that predates nothing you need, or start fresh). Keep a second copy of the admin password until then.

## 8. Uploading a backup from a phone

1. In Tryst on the phone, export a backup and note the password you chose.
2. On the same wifi, open Alcove in the phone browser, sign in, go to **My data**, choose the file, enter the password and press
   Upload. A bar shows progress, then "Checking the backup".
3. A wrong password, a file that is not a Tryst backup, a damaged file and a file already uploaded are all refused with a plain
   message, and nothing is saved. A good upload becomes your current data; older uploads are kept as history.
4. The limit is `ALCOVE_MAX_UPLOAD_MB` (default 512). A slow wifi is fine: that one request is allowed to run for up to 25 minutes.

To try Alcove with no real data, make a fake backup: `docker run --rm -v <folder>:/f <image> test-backup -out /f/test.tryst`. It prints
the password to use.

## 9. The search index or cache looks wrong

They are copies made from each person's current upload and can be thrown away. Admin console → **Backups** → **Rebuild now** (asks
for your password again). It needs each person's remembered backup password; anyone who chose not to remember it is skipped and
listed in the result. No upload is touched.
