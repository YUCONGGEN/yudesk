#!/bin/zsh
# macOS local-network privacy can deny router access to a background YuDesk
# process. Reuse the server's restricted loopback SSH key so sshd executes the
# validated UPnP command, exactly like the existing daily refresh task.
set -eu

LOOPBACK_KEY="${YUDESK_UPNPC_LOOPBACK_KEY:-/Users/yu/.ssh/frp_upnpc_loopback}"
action="${1:-}"
case "$action" in
  open|close)
    port="${2:-}"
    if [[ ! "$port" =~ ^[0-9]+$ ]] || (( port < 9000 || port > 9500 )); then
      echo "managed port must be between 9000 and 9500" >&2
      exit 2
    fi
    command=(yudesk-port "$action" "$port")
    ;;
  cleanup)
    if (( $# != 1 )); then
      echo "cleanup does not accept a port" >&2
      exit 2
    fi
    command=(yudesk-port cleanup)
    ;;
  *)
    echo "usage: $0 {open PORT|close PORT|cleanup}" >&2
    exit 2
    ;;
esac

if [[ ! -r "$LOOPBACK_KEY" ]]; then
  echo "restricted UPnP loopback key is unavailable" >&2
  exit 1
fi

exec /usr/bin/ssh -4 \
  -i "$LOOPBACK_KEY" \
  -o BatchMode=yes \
  -o IdentitiesOnly=yes \
  -o StrictHostKeyChecking=yes \
  -o ConnectTimeout=3 \
  yu@127.0.0.1 "${command[@]}"
