#!/bin/zsh
set -eu

ROOT_DIR="${YUDESK_ROOT:-$HOME/bin/yudesk}"
PID_FILE="$ROOT_DIR/run/yudesk-relay.pid"
source "$ROOT_DIR/server.conf"

cleanup_frp() {
  if [[ -x "${FRP_ROOT:-}/frp-control.sh" ]]; then
    "$FRP_ROOT/frp-control.sh" stop >/dev/null 2>&1 || true
  fi
  if [[ -x "${FRP_PORT_HOOK:-}" ]]; then
    "$FRP_PORT_HOOK" cleanup >/dev/null 2>&1 || true
  fi
}
if [[ ! -f "$PID_FILE" ]]; then
  cleanup_frp
  echo "YuDesk is not running"
  exit 0
fi
pid="$(tr -cd '0-9' < "$PID_FILE")"
if [[ -z "$pid" ]]; then
  echo "invalid PID file: $PID_FILE" >&2
  exit 1
fi
if ! kill -0 "$pid" 2>/dev/null; then
  cleanup_frp
  rm -f "$PID_FILE"
  echo "YuDesk is not running; stale PID file removed"
  exit 0
fi
command_line="$(ps -p "$pid" -o command=)"
case "$command_line" in
  "$ROOT_DIR/bin/yudesk-relay"*) ;;
  *) echo "PID $pid does not belong to YuDesk; refusing to stop it" >&2; exit 1 ;;
esac

cleanup_frp
kill "$pid"
for _ in {1..20}; do
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$PID_FILE"
    echo "YuDesk stopped"
    exit 0
  fi
  sleep 0.25
done
echo "YuDesk did not stop within 5 seconds" >&2
exit 1
