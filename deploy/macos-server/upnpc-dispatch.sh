#!/bin/zsh
# Forced command for the loopback-only frp_upnpc_loopback SSH key. Never eval
# SSH_ORIGINAL_COMMAND: split and validate the small command grammar instead.
set -eu

ROOT=/Users/yu/bin/frp
original="${SSH_ORIGINAL_COMMAND:-}"
if [[ -z "$original" ]]; then
  exec "$ROOT/upnpc-refresh.sh"
fi

set -- ${(z)original}
if (( $# == 2 )) && [[ "$1" == yudesk-port && "$2" == cleanup ]]; then
  exec "$ROOT/yudesk-port-direct.sh" cleanup
fi
if (( $# == 3 )) && [[ "$1" == yudesk-port ]] && [[ "$2" == open || "$2" == close ]] && [[ "$3" =~ ^[0-9]+$ ]] && (( $3 >= 9000 && $3 <= 9500 )); then
  exec "$ROOT/yudesk-port-direct.sh" "$2" "$3"
fi

echo "restricted UPnP command rejected" >&2
exit 2
