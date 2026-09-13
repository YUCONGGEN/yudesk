#!/bin/zsh
set -eu

UPNPC_BIN="${UPNPC_BIN:-/opt/homebrew/bin/upnpc}"
IGD_URL="${IGD_URL:-http://192.168.1.1:5431/gatedesc.xml}"
LAN_INTERFACE="${LAN_INTERFACE:-$(/sbin/route -n get default 2>/dev/null | /usr/bin/awk '/interface:/{print $2; exit}')}"
LOCAL_IP="${YUDESK_LOCAL_IP:-$(/usr/sbin/ipconfig getifaddr "$LAN_INTERFACE" 2>/dev/null || true)}"
DEFAULT_PORTS=(8230 8231 8232 8233 8235 8240 8241 8250 8251)
# Opt in explicitly; existing TCP-only invocations keep their original scope.
STUN_PORT="${YUDESK_STUN_PORT:-}"

if [[ ! -x "$UPNPC_BIN" ]]; then
  echo "upnpc not found: $UPNPC_BIN" >&2
  exit 1
fi
if [[ -z "$LOCAL_IP" ]]; then
  echo "cannot determine the LAN address for $LAN_INTERFACE" >&2
  exit 1
fi
if [[ -n "$STUN_PORT" ]] && { [[ ! "$STUN_PORT" =~ ^[0-9]+$ ]] || (( STUN_PORT < 1 || STUN_PORT > 65535 )); }; then
  echo "invalid STUN UDP port: $STUN_PORT" >&2
  exit 1
fi

ports=("$@")
if (( ${#ports[@]} == 0 )); then
  ports=("${DEFAULT_PORTS[@]}")
fi

for port in "${ports[@]}"; do
  if [[ ! "$port" =~ ^[0-9]+$ ]] || (( port < 1 || port > 65535 )); then
    echo "invalid TCP port: $port" >&2
    exit 1
  fi
  "$UPNPC_BIN" -u "$IGD_URL" -d "$port" TCP >/dev/null 2>&1 || true
  "$UPNPC_BIN" -u "$IGD_URL" -a "$LOCAL_IP" "$port" "$port" TCP
done

if [[ -n "$STUN_PORT" ]]; then
  "$UPNPC_BIN" -u "$IGD_URL" -a "$LOCAL_IP" "$STUN_PORT" "$STUN_PORT" UDP
fi

"$UPNPC_BIN" -u "$IGD_URL" -l
