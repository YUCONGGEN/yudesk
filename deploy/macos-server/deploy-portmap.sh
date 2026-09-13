#!/bin/sh
# One-time, rollback-safe installation of managed FRP on the YuDesk Mac mini.
set -eu
umask 077

root=/Users/yu/bin/yudesk
frp=/Users/yu/bin/frp
launch_agents=/Users/yu/Library/LaunchAgents
name=${1:?release name required}
expected_pid=${2:?current relay PID required}
case "$name" in ''|*[!a-zA-Z0-9._-]*|.*) exit 2;; esac
case "$expected_pid" in ''|*[!0-9]*) exit 2;; esac
stage="$root/staging/$name"
backup="$root/backups/$name"

test "$(cd "$root" && pwd -P)" = "$root"
test "$(cd "$frp" && pwd -P)" = "$frp"
test "$(cat "$root/run/yudesk-relay.pid")" = "$expected_pid"
kill -0 "$expected_pid"
test -d "$stage" && test ! -L "$stage"
for file in yudesk-relay server.conf start.sh stop.sh status.sh frps.toml frps.crt frps.key yudesk-port-hook.sh yudesk-port-direct.sh upnpc-dispatch.sh com.yu.frps.plist; do
  test -s "$stage/$file" && test ! -L "$stage/$file"
done
zsh -n "$stage/start.sh"
zsh -n "$stage/stop.sh"
zsh -n "$stage/status.sh"
zsh -n "$stage/yudesk-port-hook.sh"
zsh -n "$stage/yudesk-port-direct.sh"
zsh -n "$stage/upnpc-dispatch.sh"
plutil -lint "$stage/com.yu.frps.plist" >/dev/null
"$frp/frps" verify -c "$stage/frps.toml" >/dev/null
/usr/bin/openssl x509 -in "$stage/frps.crt" -noout -text | grep -F 'DNS:www.yucg.cn' >/dev/null

first=$(/usr/bin/curl --noproxy '*' -fsS http://127.0.0.1:8235/api/public-stats)
sleep 2
second=$(/usr/bin/curl --noproxy '*' -fsS http://127.0.0.1:8235/api/public-stats)
for snapshot in "$first" "$second"; do
  case "$snapshot" in
    *'"connectedDevices":0'*'"activeSessions":0'*) ;;
    *) echo "Active session detected; deployment postponed: $snapshot" >&2; exit 1;;
  esac
done

test ! -e "$backup"
mkdir "$backup"
cp -p "$root/bin/yudesk-relay" "$root/server.conf" "$root/start.sh" "$root/stop.sh" "$root/status.sh" "$backup/"
cp -p "$frp/frps.toml" "$frp/frp-control.sh" "$backup/"
cp -p "$launch_agents/com.yu.frps.plist" "$backup/"
cp -p /Users/yu/.ssh/authorized_keys "$backup/authorized_keys"
test ! -L "$root/bin/yudesk-relay"
test ! -L "$root/server.conf"
test ! -L "$root/start.sh"
test ! -L "$root/stop.sh"
test ! -L "$root/status.sh"
test ! -L "$frp/frps.toml"
test ! -L "$launch_agents/com.yu.frps.plist"

stopped=0
committed=0
finish() {
  result=$?
  trap - EXIT
  if [ "$result" != 0 ] && [ "$stopped" = 1 ] && [ "$committed" = 0 ]; then
    set +e
    "$root/stop.sh"
    "$frp/frp-control.sh" stop
    for file in server.conf start.sh stop.sh status.sh; do
      cp -p "$backup/$file" "$root/$file.rollback"
      mv "$root/$file.rollback" "$root/$file"
    done
    cp -p "$backup/yudesk-relay" "$root/bin/yudesk-relay.rollback"
    mv "$root/bin/yudesk-relay.rollback" "$root/bin/yudesk-relay"
    cp -p "$backup/frps.toml" "$frp/frps.toml.rollback"
    mv "$frp/frps.toml.rollback" "$frp/frps.toml"
    cp -p "$backup/com.yu.frps.plist" "$launch_agents/com.yu.frps.plist.rollback"
    mv "$launch_agents/com.yu.frps.plist.rollback" "$launch_agents/com.yu.frps.plist"
    cp -p "$backup/authorized_keys" /Users/yu/.ssh/authorized_keys.rollback
    mv /Users/yu/.ssh/authorized_keys.rollback /Users/yu/.ssh/authorized_keys
    chmod 700 "$root/start.sh" "$root/stop.sh" "$root/status.sh" "$root/bin/yudesk-relay"
    "$root/start.sh"
    echo "Managed FRP deployment failed; previous YuDesk server restored from $backup" >&2
  fi
  exit "$result"
}
trap finish EXIT

"$root/stop.sh"
stopped=1
cp "$stage/yudesk-relay" "$root/bin/yudesk-relay.next"
chmod 700 "$root/bin/yudesk-relay.next"
mv "$root/bin/yudesk-relay.next" "$root/bin/yudesk-relay"
for file in server.conf start.sh stop.sh status.sh; do
  cp "$stage/$file" "$root/$file.next"
  chmod 700 "$root/$file.next"
  test "$file" != server.conf || chmod 600 "$root/$file.next"
  mv "$root/$file.next" "$root/$file"
done
for file in frps.toml frps.crt frps.key yudesk-port-hook.sh yudesk-port-direct.sh upnpc-dispatch.sh; do
  cp "$stage/$file" "$frp/$file.next"
  chmod 600 "$frp/$file.next"
  case "$file" in yudesk-port-hook.sh|yudesk-port-direct.sh|upnpc-dispatch.sh) chmod 700 "$frp/$file.next";; esac
  mv "$frp/$file.next" "$frp/$file"
done
old_forced='command="/Users/yu/bin/frp/upnpc-refresh.sh"'
new_forced='command="/Users/yu/bin/frp/upnpc-dispatch.sh"'
test "$(grep -Fc "$old_forced" /Users/yu/.ssh/authorized_keys)" = 1
sed "s#$old_forced#$new_forced#" /Users/yu/.ssh/authorized_keys > /Users/yu/.ssh/authorized_keys.next
chmod 600 /Users/yu/.ssh/authorized_keys.next
mv /Users/yu/.ssh/authorized_keys.next /Users/yu/.ssh/authorized_keys
cp "$stage/com.yu.frps.plist" "$launch_agents/com.yu.frps.plist.next"
chmod 600 "$launch_agents/com.yu.frps.plist.next"
mv "$launch_agents/com.yu.frps.plist.next" "$launch_agents/com.yu.frps.plist"

"$root/start.sh"
ready=0
attempt=0
while [ "$attempt" -lt 20 ]; do
  if /usr/bin/curl --noproxy '*' --connect-timeout 1 --max-time 1 -fsS http://127.0.0.1:8235/healthz >/dev/null 2>&1; then
    ready=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.25
done
test "$ready" = 1
relay_pid=$(cat "$root/run/yudesk-relay.pid")
kill -0 "$relay_pid"
/usr/sbin/lsof -nP -a -p "$relay_pid" -iTCP:8236 -sTCP:LISTEN >/dev/null
/usr/sbin/lsof -nP -iTCP:8232 -sTCP:LISTEN | grep -F frps >/dev/null
"$frp/frp-control.sh" status >/dev/null
test -z "$(/opt/homebrew/bin/upnpc -u http://192.168.1.1:5431/gatedesc.xml -l 2>/dev/null | awk '$2 == "TCP" && $3 ~ /^(9[0-4][0-9][0-9]|9500)->/ {print}')"
/usr/bin/curl --noproxy '*' -fsS http://127.0.0.1:8235/api/public-stats >/dev/null

committed=1
echo "Managed FRP deployed; backup: $backup"
"$root/status.sh"
