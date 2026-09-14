#!/bin/sh
# Explicit deployment on the configured YuDesk host. Runtime/business history
# is reset on every successful release; server identity and admin access remain.
set -eu
umask 077
root=/Users/yu/bin/yudesk
name=${1:?release name required}
archive_hash=${2:?archive SHA-256 required}
expected_pid=${3:?current relay PID required}
case "$name" in ''|*[!a-zA-Z0-9._-]*|.*) exit 2;; esac
case "$archive_hash" in *[!0-9a-f]*) exit 2;; esac
test "${#archive_hash}" = 64
case "$expected_pid" in ''|*[!0-9]*) exit 2;; esac
test "$(cd "$root" && pwd -P)" = "$root"
for dir in bin data downloads releases staging backups run logs; do
  test -d "$root/$dir" && test ! -L "$root/$dir"
done
test -z "$(find "$root/downloads" -type l -print)"
test ! -L "$root/bin/yudesk-relay"
test "$(cat "$root/run/yudesk-relay.pid")" = "$expected_pid"
kill -0 "$expected_pid"
first_stats=$(/usr/bin/curl --noproxy '*' -fsS http://127.0.0.1:8235/api/public-stats)
sleep 2
second_stats=$(/usr/bin/curl --noproxy '*' -fsS http://127.0.0.1:8235/api/public-stats)
for snapshot in "$first_stats" "$second_stats"; do
  case "$snapshot" in
    *'"connectedDevices":0'*'"activeSessions":0'*) ;;
    *) echo "Active session detected; deployment postponed: $snapshot" >&2; exit 1;;
  esac
done
archive="$root/staging/$name.tgz"
config_next="$root/staging/$name-server.conf"
start_next="$root/staging/$name-start.sh"
release="$root/releases/$name"
backup="$root/backups/$name"
test -f "$archive" && test ! -L "$archive"
test -f "$config_next" && test ! -L "$config_next"
test -f "$start_next" && test ! -L "$start_next"
zsh -n "$start_next"
grep -q '^TURN_PORT=' "$config_next"
grep -q '^TURN_RELAY_MIN_PORT=' "$config_next"
grep -q '^TURN_RELAY_MAX_PORT=' "$config_next"
test ! -e "$release" && test ! -e "$backup"
test "$(shasum -a 256 "$archive" | awk '{print $1}')" = "$archive_hash"
files='windows-amd64/yudesk.exe linux-amd64/yudesk linux-amd64/yudesk.deb linux-amd64/yudesk.provenance.json darwin-amd64/yudesk darwin-amd64/yudesk.pkg darwin-amd64/yudesk.provenance.json darwin-arm64/yudesk darwin-arm64/yudesk.pkg darwin-arm64/yudesk.provenance.json android/yudesk.apk desktop-core-builds.json SHA256SUMS.txt release.json THIRD_PARTY_NOTICES.txt'
tar -tzf "$archive" | while IFS= read -r entry; do
  case "$entry" in
    server/yudesk-relay|RELEASE-SHA256SUMS.txt) ;;
    downloads/windows-amd64/yudesk.exe|downloads/linux-amd64/yudesk|downloads/darwin-amd64/yudesk|downloads/darwin-arm64/yudesk|downloads/android/yudesk.apk|downloads/desktop-core-builds.json|downloads/SHA256SUMS.txt|downloads/release.json|downloads/THIRD_PARTY_NOTICES.txt) ;;
    downloads/linux-amd64/yudesk.deb|downloads/linux-amd64/yudesk.provenance.json|downloads/darwin-amd64/yudesk.pkg|downloads/darwin-amd64/yudesk.provenance.json|downloads/darwin-arm64/yudesk.pkg|downloads/darwin-arm64/yudesk.provenance.json) ;;
    *) echo "Unexpected archive entry: $entry" >&2; exit 1;;
  esac
done
mkdir "$release" "$backup"
tar -xzf "$archive" -C "$release"
test -z "$(find "$release" -type l -print)"
(cd "$release" && shasum -a 256 -c RELEASE-SHA256SUMS.txt)
cp -p "$root/bin/yudesk-relay" "$backup/yudesk-relay"
cp -p "$root/server.conf" "$backup/server.conf"
cp -p "$root/start.sh" "$backup/start.sh"
cp -Rp "$root/downloads" "$backup/downloads"
for file in $files; do
  test -f "$release/downloads/$file"
  test ! -e "$root/downloads/$file.next"
done
test ! -e "$root/bin/yudesk-relay.next"
stopped=0
committed=0
finish() {
  result=$?
  trap - EXIT
  if [ "$result" != 0 ] && [ "$stopped" = 1 ] && [ "$committed" = 0 ]; then
    set +e
    "$root/stop.sh"
    cp -p "$backup/server.conf" "$root/server.conf.rollback"
    mv "$root/server.conf.rollback" "$root/server.conf"
    cp -p "$backup/start.sh" "$root/start.sh.rollback"
    chmod 700 "$root/start.sh.rollback"
    mv "$root/start.sh.rollback" "$root/start.sh"
    cp -p "$backup/yudesk-relay" "$root/bin/yudesk-relay.rollback"
    mv "$root/bin/yudesk-relay.rollback" "$root/bin/yudesk-relay"
    for file in $files; do
      if [ -f "$backup/downloads/$file" ]; then
        cp -p "$backup/downloads/$file" "$root/downloads/$file.rollback"
        mv "$root/downloads/$file.rollback" "$root/downloads/$file"
      else
        # Only the fixed, validated release-file list above is eligible. Remove
        # new installer files on rollback if the previous release had none.
        rm -f "$root/downloads/$file"
      fi
    done
    "$root/start.sh"
    echo "Deployment failed; old executable/downloads restored. Business data remains reset. Temporary rollback: $backup" >&2
  fi
  exit "$result"
}
trap finish EXIT
"$root/stop.sh"
stopped=1
# The release policy intentionally starts with no devices, activations,
# meetings, mappings, audit records or sessions. Do not copy this database to
# backups. TLS keys, TURN secret and admin.key are separate files and survive.
rm -f "$root/data/yudesk.db" "$root/data/yudesk.db-wal" "$root/data/yudesk.db-shm"
find "$root/logs" -mindepth 1 -maxdepth 1 -type f -delete
cp "$release/server/yudesk-relay" "$root/bin/yudesk-relay.next"
chmod 700 "$root/bin/yudesk-relay.next"
mv "$root/bin/yudesk-relay.next" "$root/bin/yudesk-relay"
cp "$config_next" "$root/server.conf.next"
chmod 600 "$root/server.conf.next"
mv "$root/server.conf.next" "$root/server.conf"
cp "$start_next" "$root/start.sh.next"
chmod 700 "$root/start.sh.next"
mv "$root/start.sh.next" "$root/start.sh"
for file in $files; do
  mkdir -p "$(dirname "$root/downloads/$file")"
  cp "$release/downloads/$file" "$root/downloads/$file.next"
  chmod 644 "$root/downloads/$file.next"
  mv "$root/downloads/$file.next" "$root/downloads/$file"
done
"$root/start.sh"
# start.sh returning a PID does not mean the HTTP listener has bound yet.
# Keep rollback armed, but allow bounded startup time before checking assets.
ready=0
attempt=0
while [ "$attempt" -lt 20 ]; do
  if /usr/bin/curl --noproxy '*' --connect-timeout 1 --max-time 1 -fsS 'http://127.0.0.1:8235/healthz' >/dev/null 2>&1; then
    ready=1
    break
  fi
  kill -0 "$(cat "$root/run/yudesk-relay.pid")" || break
  attempt=$((attempt + 1))
  sleep 1
done
test "$ready" = 1
for route in /healthz / /download/windows-amd64/yudesk.exe /download/linux-amd64/yudesk.deb /download/darwin-amd64/yudesk.pkg /download/darwin-arm64/yudesk.pkg /download/android/yudesk.apk /SHA256SUMS.txt /THIRD_PARTY_NOTICES.txt; do
  /usr/bin/curl --noproxy '*' --connect-timeout 3 --max-time 15 -fsSI "http://127.0.0.1:8235$route" >/dev/null
done
for file in $files; do
  test "$(shasum -a 256 "$root/downloads/$file" | awk '{print $1}')" = "$(shasum -a 256 "$release/downloads/$file" | awk '{print $1}')"
done
test "$(shasum -a 256 "$root/bin/yudesk-relay" | awk '{print $1}')" = "$(shasum -a 256 "$release/server/yudesk-relay" | awk '{print $1}')"
test "$(/usr/bin/sqlite3 "$root/data/yudesk.db" 'PRAGMA quick_check;')" = ok
lsof -nP -a -p "$(cat "$root/run/yudesk-relay.pid")" -iUDP:8233 >/dev/null
lsof -nP -a -p "$(cat "$root/run/yudesk-relay.pid")" -iUDP:8254 >/dev/null
lsof -nP -a -p "$(cat "$root/run/yudesk-relay.pid")" -iTCP:8254 >/dev/null
committed=1
for history in "$root/releases" "$root/backups" "$root/staging"; do
  test "$(cd "$history" && pwd -P)" = "$history"
  find "$history" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
done
echo "Deployed $name; business and deployment history cleared"
"$root/status.sh"
cat "$root/downloads/release.json"
