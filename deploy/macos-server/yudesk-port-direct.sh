#!/bin/zsh
# Direct router operation. This script is reachable only through the restricted
# loopback SSH forced command in upnpc-dispatch.sh.
set -eu

umask 077
ROOT_DIR="${YUDESK_ROOT:-$HOME/bin/yudesk}"
UPNPC_BIN="${UPNPC_BIN:-/opt/homebrew/bin/upnpc}"
IGD_URL="${IGD_URL:-http://192.168.1.1:5431/gatedesc.xml}"
LAN_INTERFACE="${LAN_INTERFACE:-$(/sbin/route -n get default 2>/dev/null | /usr/bin/awk '/interface:/{print $2; exit}')}"
LOCAL_IP="${YUDESK_LOCAL_IP:-$(/usr/sbin/ipconfig getifaddr "$LAN_INTERFACE" 2>/dev/null || true)}"
STATE_FILE="${YUDESK_PORT_STATE:-$ROOT_DIR/run/yudesk-frp-ports}"
HOOK_LOG="${YUDESK_PORT_HOOK_LOG:-$ROOT_DIR/logs/yudesk-port-map.log}"

if [[ ! -x "$UPNPC_BIN" ]]; then
  echo "upnpc not found: $UPNPC_BIN" >&2
  exit 1
fi
if [[ -z "$LOCAL_IP" ]]; then
  echo "cannot determine the LAN address for $LAN_INTERFACE" >&2
  exit 1
fi
mkdir -p "${STATE_FILE:h}" "${HOOK_LOG:h}"
touch "$STATE_FILE"

log_event() {
  printf '%s action=%s port=%s pid=%s %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "${action:-startup}" "${port:-none}" "$$" "$1" >> "$HOOK_LOG"
}

valid_port() {
  [[ "$1" =~ ^[0-9]+$ ]] && (( $1 >= 9000 && $1 <= 9500 ))
}

replace_state_without() {
  local removed="$1"
  local temporary
  temporary="$(mktemp "$STATE_FILE.XXXXXX")"
  /usr/bin/awk -v removed="$removed" '$0 != removed && $0 ~ /^[0-9]+$/ { print $0 }' "$STATE_FILE" | /usr/bin/sort -nu > "$temporary"
  mv "$temporary" "$STATE_FILE"
}

remember_port() {
  local port="$1"
  if ! /usr/bin/grep -qx "$port" "$STATE_FILE"; then
    echo "$port" >> "$STATE_FILE"
  fi
  local temporary
  temporary="$(mktemp "$STATE_FILE.XXXXXX")"
  /usr/bin/sort -nu "$STATE_FILE" > "$temporary"
  mv "$temporary" "$STATE_FILE"
}

close_port() {
  local close_output
  if close_output="$("$UPNPC_BIN" -u "$IGD_URL" -d "$1" TCP 2>&1)"; then
    log_event "close=ok"
    return 0
  fi
  log_event "close=failed detail=$(printf '%s' "$close_output" | tr '\n' ' ' | cut -c1-240)"
  printf '%s\n' "$close_output" >&2
  return 1
}

action="${1:-}"
case "$action" in
  open)
    port="${2:-}"
    if ! valid_port "$port"; then
      echo "managed port must be between 9000 and 9500" >&2
      exit 2
    fi
    open_output=""
    if ! open_output="$("$UPNPC_BIN" -u "$IGD_URL" -e "YuDesk managed port" -a "$LOCAL_IP" "$port" "$port" TCP 2>&1)"; then
      log_event "open=failed detail=$(printf '%s' "$open_output" | tr '\n' ' ' | cut -c1-240)"
      printf '%s\n' "$open_output" >&2
      exit 1
    fi
    remember_port "$port"
    log_event "open=ok local=$LOCAL_IP"
    ;;
  close)
    port="${2:-}"
    if ! valid_port "$port"; then
      echo "managed port must be between 9000 and 9500" >&2
      exit 2
    fi
    close_port "$port"
    replace_state_without "$port"
    ;;
  cleanup)
    failed="$(mktemp "$STATE_FILE.failed.XXXXXX")"
    while IFS= read -r port; do
      if ! valid_port "$port"; then
        continue
      fi
      if ! close_port "$port"; then
        echo "$port" >> "$failed"
      fi
    done < "$STATE_FILE"
    /usr/bin/sort -nu "$failed" > "$STATE_FILE"
    rm -f "$failed"
    if [[ -s "$STATE_FILE" ]]; then
      echo "some stale YuDesk ports could not be removed" >&2
      exit 1
    fi
    ;;
  *)
    echo "usage: $0 {open PORT|close PORT|cleanup}" >&2
    exit 2
    ;;
esac
