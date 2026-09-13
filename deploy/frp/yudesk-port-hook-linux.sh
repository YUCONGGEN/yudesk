#!/bin/sh
# Optional NAT helper for Linux servers behind an UPnP router. Public Linux
# hosts do not need this hook; allow TCP 8232 and 9000:9500 in their firewall.
set -eu

umask 077
root_dir=${YUDESK_ROOT:-/opt/yudesk}
upnpc_bin=${UPNPC_BIN:-/usr/bin/upnpc}
state_file=${YUDESK_PORT_STATE:-$root_dir/run/yudesk-frp-ports}
local_ip=${YUDESK_LOCAL_IP:-$(ip -4 route get 1.1.1.1 | awk '/src/{for(i=1;i<=NF;i++) if($i=="src") {print $(i+1); exit}}')}
mkdir -p "$(dirname "$state_file")"
touch "$state_file"

valid_port() {
  case "$1" in ''|*[!0-9]*) return 1;; esac
  test "$1" -ge 9000 && test "$1" -le 9500
}

action=${1:-}
case "$action" in
  open)
    port=${2:-}
    valid_port "$port" || exit 2
    "$upnpc_bin" -e "YuDesk managed port" -a "$local_ip" "$port" "$port" TCP >/dev/null
    { grep -vx "$port" "$state_file" || true; echo "$port"; } | sort -nu > "$state_file.next"
    mv "$state_file.next" "$state_file"
    ;;
  close)
    port=${2:-}
    valid_port "$port" || exit 2
    "$upnpc_bin" -d "$port" TCP >/dev/null
    grep -vx "$port" "$state_file" > "$state_file.next" || true
    mv "$state_file.next" "$state_file"
    ;;
  cleanup)
    : > "$state_file.failed"
    while IFS= read -r port; do
      valid_port "$port" || continue
      "$upnpc_bin" -d "$port" TCP >/dev/null || echo "$port" >> "$state_file.failed"
    done < "$state_file"
    sort -nu "$state_file.failed" > "$state_file"
    rm -f "$state_file.failed"
    test ! -s "$state_file"
    ;;
  *)
    echo "usage: $0 {open PORT|close PORT|cleanup}" >&2
    exit 2
    ;;
esac
