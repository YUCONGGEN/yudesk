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
: "${TURN_PORT:?TURN_PORT is required}"
: "${TURN_RELAY_MIN_PORT:?TURN_RELAY_MIN_PORT is required}"
: "${TURN_RELAY_MAX_PORT:?TURN_RELAY_MAX_PORT is required}"
: "${FRP_CONTROL_PORT:?FRP_CONTROL_PORT is required}"
: "${FRP_PLUGIN_PORT:?FRP_PLUGIN_PORT is required}"
: "${FRP_ROOT:?FRP_ROOT is required}"
: "${FRP_CERT:?FRP_CERT is required}"
: "${FRP_PORT_HOOK:?FRP_PORT_HOOK is required}"

for required_file in "$FRP_CERT" "$FRP_PORT_HOOK" "$FRP_ROOT/yudesk-port-direct.sh" "$FRP_ROOT/upnpc-dispatch.sh" "$FRP_ROOT/frps" "$FRP_ROOT/frps.toml" "$FRP_ROOT/frp-control.sh"; do
  if [[ ! -f "$required_file" ]]; then
    echo "missing FRP component: $required_file" >&2
    exit 1
  fi
done

mkdir -p "$ROOT_DIR/data" "$ROOT_DIR/downloads" "$ROOT_DIR/logs" "$ROOT_DIR/run"
if [[ -f "$PID_FILE" ]]; then
  old_pid="$(tr -cd '0-9' < "$PID_FILE")"
  if [[ -n "$old_pid" ]] && kill -0 "$old_pid" 2>/dev/null; then
    "$FRP_ROOT/frp-control.sh" start
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
  -conference-turn ":$TURN_PORT" \
  -conference-turn-public "$PUBLIC_HOST:$TURN_PORT" \
  -conference-turn-secret-file "$ROOT_DIR/data/conference-turn.secret" \
  -conference-turn-relay-min-port "$TURN_RELAY_MIN_PORT" \
  -conference-turn-relay-max-port "$TURN_RELAY_MAX_PORT" \
  -conference-direct-timeout-ms 3000 \
  -frp-public "$PUBLIC_HOST:$FRP_CONTROL_PORT" \
  -frp-plugin-http "127.0.0.1:$FRP_PLUGIN_PORT" \
  -frp-cert "$FRP_CERT" \
  -frp-port-hook "$FRP_PORT_HOOK" \
  >>"$LOG_FILE" 2>&1 &
pid=$!
echo "$pid" > "$PID_FILE"

sleep 1
if ! kill -0 "$pid" 2>/dev/null; then
  echo "YuDesk failed to start; recent log output:" >&2
  tail -n 30 "$LOG_FILE" >&2 || true
  exit 1
fi
if ! "$FRP_ROOT/frp-control.sh" start; then
  echo "FRPS failed to start; stopping YuDesk to avoid a partial service" >&2
  kill "$pid" 2>/dev/null || true
  "$FRP_PORT_HOOK" cleanup >/dev/null 2>&1 || true
  rm -f "$PID_FILE"
  exit 1
fi
frp_ready=0
for _ in {1..20}; do
  frp_pid="$(/usr/sbin/lsof -nP -t -iTCP:"$FRP_CONTROL_PORT" -sTCP:LISTEN 2>/dev/null | /usr/bin/head -n 1 || true)"
  if [[ -n "$frp_pid" ]]; then
    frp_command="$(ps -p "$frp_pid" -o command= 2>/dev/null || true)"
    if [[ "$frp_command" == "$FRP_ROOT/frps"* ]]; then
      frp_ready=1
      break
    fi
  fi
  sleep 0.25
done
if [[ "$frp_ready" != 1 ]]; then
  echo "FRPS did not bind TCP $FRP_CONTROL_PORT; stopping the partial service" >&2
  "$FRP_ROOT/frp-control.sh" stop >/dev/null 2>&1 || true
  kill "$pid" 2>/dev/null || true
  "$FRP_PORT_HOOK" cleanup >/dev/null 2>&1 || true
  rm -f "$PID_FILE"
  exit 1
fi
public_web_url="http://$PUBLIC_HOST"
if [[ "$PUBLIC_HTTP_PORT" != "80" ]]; then
  public_web_url="$public_web_url:$PUBLIC_HTTP_PORT"
fi
echo "YuDesk started (PID $pid, relay $PUBLIC_HOST:$RELAY_PORT, FRP $PUBLIC_HOST:$FRP_CONTROL_PORT, meeting TURN $PUBLIC_HOST:$TURN_PORT, website $public_web_url/)"
