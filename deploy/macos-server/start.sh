#!/bin/zsh
set -eu

umask 077
ROOT_DIR="${YUDESK_ROOT:-$HOME/bin/yudesk}"
CONFIG_FILE="$ROOT_DIR/server.conf"
PID_FILE="$ROOT_DIR/run/yudesk-relay.pid"
LOG_FILE="$ROOT_DIR/logs/yudesk-relay.log"

if [[ ! -f "$CONFIG_FILE" ]]; then
  echo "missing configuration: $CONFIG_FILE" >&2
  exit 1
fi
source "$CONFIG_FILE"
: "${RELAY_PORT:?RELAY_PORT is required}"
: "${WEB_PORT:?WEB_PORT is required}"
: "${PUBLIC_HTTP_PORT:?PUBLIC_HTTP_PORT is required}"
: "${PUBLIC_HOST:?PUBLIC_HOST is required}"

mkdir -p "$ROOT_DIR/data" "$ROOT_DIR/downloads" "$ROOT_DIR/logs" "$ROOT_DIR/run"
if [[ -f "$PID_FILE" ]]; then
  old_pid="$(tr -cd '0-9' < "$PID_FILE")"
  if [[ -n "$old_pid" ]] && kill -0 "$old_pid" 2>/dev/null; then
    echo "YuDesk is already running (PID $old_pid)"
    exit 0
  fi
  rm -f "$PID_FILE"
fi

nohup "$ROOT_DIR/bin/yudesk-relay" \
  -listen ":$RELAY_PORT" \
  -http ":$WEB_PORT" \
  -device-licenses \
  -admin-key-file "$ROOT_DIR/data/admin.key" \
  -public-relay "$PUBLIC_HOST:$RELAY_PORT" \
  -accounts "$ROOT_DIR/data/yudesk.db" \
  -cert "$ROOT_DIR/data/relay.crt" \
  -key "$ROOT_DIR/data/relay.key" \
  -downloads "$ROOT_DIR/downloads" \
  >>"$LOG_FILE" 2>&1 &
pid=$!
echo "$pid" > "$PID_FILE"

sleep 1
if ! kill -0 "$pid" 2>/dev/null; then
  echo "YuDesk failed to start; recent log output:" >&2
  tail -n 30 "$LOG_FILE" >&2 || true
  exit 1
fi
public_web_url="http://$PUBLIC_HOST"
if [[ "$PUBLIC_HTTP_PORT" != "80" ]]; then
  public_web_url="$public_web_url:$PUBLIC_HTTP_PORT"
fi
echo "YuDesk started (PID $pid, relay $PUBLIC_HOST:$RELAY_PORT, website $public_web_url/)"
