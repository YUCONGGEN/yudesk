#!/bin/sh
# Confirm an idle, unchanged server and prepare immutable deployment inputs.
set -eu
umask 077

root=/Users/yu/bin/yudesk
name=${1:?release name required}
expected_pid=${2:?current relay PID required}
case "$name" in ''|*[!a-zA-Z0-9._-]*|.*) exit 2;; esac
case "$expected_pid" in ''|*[!0-9]*) exit 2;; esac
test "$(cd "$root" && pwd -P)" = "$root"
cd "$root"
test "$(cat run/yudesk-relay.pid)" = "$expected_pid"
kill -0 "$expected_pid"

first=$(/usr/bin/curl --noproxy '*' -fsS http://127.0.0.1:8235/api/public-stats)
sleep 2
second=$(/usr/bin/curl --noproxy '*' -fsS http://127.0.0.1:8235/api/public-stats)
for snapshot in "$first" "$second"; do
  case "$snapshot" in
    *'"connectedDevices":0'*'"activeSessions":0'*) ;;
    *) echo "Active session detected: $snapshot" >&2; exit 1;;
  esac
done

for path in \
  "$root/staging/$name.tgz" \
  "$root/staging/$name-server.conf" \
  "$root/staging/$name-start.sh" \
  "$root/staging/$name-deploy-release.sh" \
  "$root/releases/$name" \
  "$root/backups/$name"; do
  test ! -e "$path"
done
cp "$root/server.conf" "$root/staging/$name-server.conf"
cp "$root/start.sh" "$root/staging/$name-start.sh"

printf 'STATS1=%s\nSTATS2=%s\nDB=' "$first" "$second"
/usr/bin/sqlite3 "$root/data/yudesk.db" 'PRAGMA quick_check; SELECT count(*) FROM accounts; SELECT count(*) FROM activation_keys; SELECT count(*) FROM licensed_devices;'
printf 'CONFIG='
/usr/bin/shasum -a 256 "$root/server.conf" | /usr/bin/awk '{print $1}'
printf 'CERT='
/usr/bin/openssl x509 -in "$root/data/relay.crt" -noout -fingerprint -sha256
