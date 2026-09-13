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
    if [[ -x "${FRP_ROOT:-}/frp-control.sh" ]] && "$FRP_ROOT/frp-control.sh" status >/dev/null 2>&1; then
      echo "Port mapping: $PUBLIC_HOST:$FRP_CONTROL_PORT (TCP 9000-9500 managed on demand)"
    else
      echo "Port mapping: FRPS is not running"
    fi
    router_listing=""
    if [[ -x /opt/homebrew/bin/upnpc ]]; then
      router_listing="$(/opt/homebrew/bin/upnpc -u "${IGD_URL:-http://192.168.1.1:5431/gatedesc.xml}" -l 2>&1 || true)"
    fi
    if /usr/sbin/lsof -nP -a -p "$pid" -iUDP:"$RELAY_PORT" >/dev/null 2>&1; then
      if [[ -n "$router_listing" ]] && /usr/bin/grep -Eq "UDP[[:space:]]+$RELAY_PORT->.+:$RELAY_PORT" <<<"$router_listing"; then
        echo "P2P STUN: UDP $RELAY_PORT is listening and mapped"
      else
        echo "P2P STUN: UDP $RELAY_PORT is listening, but the router mapping was not found"
      fi
    else
      echo "P2P STUN: UDP $RELAY_PORT is not listening"
    fi
    if [[ -n "$router_listing" ]] &&
      /usr/bin/grep -Eq "TCP[[:space:]]+$TURN_PORT->.+:$TURN_PORT" <<<"$router_listing" &&
      /usr/bin/grep -Eq "UDP[[:space:]]+$TURN_PORT->.+:$TURN_PORT" <<<"$router_listing" &&
      /usr/bin/grep -Eq "UDP[[:space:]]+$TURN_RELAY_MIN_PORT->.+:$TURN_RELAY_MIN_PORT" <<<"$router_listing" &&
      /usr/bin/grep -Eq "UDP[[:space:]]+$TURN_RELAY_MAX_PORT->.+:$TURN_RELAY_MAX_PORT" <<<"$router_listing"; then
      echo "Meeting TURN: TCP/UDP $TURN_PORT and UDP $TURN_RELAY_MIN_PORT-$TURN_RELAY_MAX_PORT are mapped"
    else
      echo "Meeting TURN: one or more public router mappings are missing"
    fi
    echo "Website: $public_web_url/ (router TCP $PUBLIC_HTTP_PORT -> local $WEB_PORT)"
    echo "Admin: $public_web_url/admin"
    exit 0
  fi
fi
echo "YuDesk is not running"
exit 1
