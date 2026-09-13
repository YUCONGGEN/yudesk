#!/bin/zsh
# Forced command for the loopback-only frp_upnpc_loopback SSH key. Never eval
# SSH_ORIGINAL_COMMAND: split and validate the small command grammar instead.
set -eu

ROOT=/Users/yu/bin/frp
original="${SSH_ORIGINAL_COMMAND:-}"
if [[ -z "$original" ]]; then
  # The scheduled loopback refresh is the owner of YuDesk's public ingress.
  # Keep UDP STUN next to the relay's TCP mapping so a router reboot cannot
  # silently force every desktop session back through the TCP relay.
  export YUDESK_STUN_PORT="${YUDESK_STUN_PORT:-8233}"
  "$ROOT/upnpc-refresh.sh"
  # TURN needs both its signaling port and the advertised UDP allocation
  # range. Keep these mappings in the same restricted scheduled refresh so
  # meetings continue to carry audio/video after a router reboot.
  exec /usr/bin/python3 /Users/yu/bin/yudesk/upnpc-conference-turn.py
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
