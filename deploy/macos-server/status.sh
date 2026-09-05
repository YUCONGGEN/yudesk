#!/bin/zsh
set -eu

ROOT_DIR="${YUDESK_ROOT:-$HOME/bin/yudesk}"
source "$ROOT_DIR/server.conf"
PID_FILE="$ROOT_DIR/run/yudesk-relay.pid"
public_web_url="http://$PUBLIC_HOST"
if [[ "$PUBLIC_HTTP_PORT" != "80" ]]; then
  public_web_url="$public_web_url:$PUBLIC_HTTP_PORT"
fi
if [[ -f "$PID_FILE" ]]; then
  pid="$(tr -cd '0-9' < "$PID_FILE")"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    echo "YuDesk is running (PID $pid)"
    echo "Relay: $PUBLIC_HOST:$RELAY_PORT"
    echo "Website: $public_web_url/ (router TCP $PUBLIC_HTTP_PORT -> local $WEB_PORT)"
    echo "Admin: $public_web_url/admin"
    exit 0
  fi
fi
echo "YuDesk is not running"
exit 1
