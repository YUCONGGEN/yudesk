#!/bin/sh
# Explicit, backed-up deployment on the configured YuDesk host. No router edits.
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
for dir in bin data downloads releases staging backups run; do
  test -d "$root/$dir" && test ! -L "$root/$dir"
done
test -z "$(find "$root/downloads" -type l -print)"
test ! -L "$root/bin/yudesk-relay"
test "$(cat "$root/run/yudesk-relay.pid")" = "$expected_pid"
kill -0 "$expected_pid"
archive="$root/staging/$name.tgz"
release="$root/releases/$name"
backup="$root/backups/$name"
test -f "$archive" && test ! -L "$archive"
test ! -e "$release" && test ! -e "$backup"
test "$(shasum -a 256 "$archive" | awk '{print $1}')" = "$archive_hash"
files='windows-amd64/yudesk.exe linux-amd64/yudesk linux-amd64/yudesk.deb darwin-amd64/yudesk darwin-amd64/yudesk.pkg darwin-arm64/yudesk darwin-arm64/yudesk.pkg android/yudesk.apk SHA256SUMS.txt release.json THIRD_PARTY_NOTICES.txt'
tar -tzf "$archive" | while IFS= read -r entry; do
  case "$entry" in
    server/yudesk-relay|RELEASE-SHA256SUMS.txt) ;;
    downloads/windows-amd64/yudesk.exe|downloads/linux-amd64/yudesk|downloads/darwin-amd64/yudesk|downloads/darwin-arm64/yudesk|downloads/android/yudesk.apk|downloads/SHA256SUMS.txt|downloads/release.json|downloads/THIRD_PARTY_NOTICES.txt) ;;
    downloads/linux-amd64/yudesk.deb|downloads/darwin-amd64/yudesk.pkg|downloads/darwin-arm64/yudesk.pkg) ;;
    *) echo "Unexpected archive entry: $entry" >&2; exit 1;;
  esac
done
mkdir "$release" "$backup"
tar -xzf "$archive" -C "$release"
test -z "$(find "$release" -type l -print)"
(cd "$release" && shasum -a 256 -c RELEASE-SHA256SUMS.txt)
cp -p "$root/bin/yudesk-relay" "$backup/yudesk-relay"
cp -p "$root/server.conf" "$backup/server.conf"
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
    echo "Deployment failed; old executable/downloads restored. Database was not replaced. Backup: $backup" >&2
  fi
  exit "$result"
}
trap finish EXIT
"$root/stop.sh"
stopped=1
/usr/bin/sqlite3 "$root/data/yudesk.db" ".backup '$backup/yudesk.db'"
test "$(/usr/bin/sqlite3 "$backup/yudesk.db" 'PRAGMA quick_check;')" = ok
cp "$release/server/yudesk-relay" "$root/bin/yudesk-relay.next"
chmod 700 "$root/bin/yudesk-relay.next"
mv "$root/bin/yudesk-relay.next" "$root/bin/yudesk-relay"
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
committed=1
echo "Deployed $name; backup: $backup"
"$root/status.sh"
cat "$root/downloads/release.json"
