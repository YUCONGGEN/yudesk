#!/bin/sh
set -eu
# Always allocate a private display; never inherit the physical host display.
test -f /.dockerenv
test "$(id -u)" != 0
src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
helper=${1:?existing helper required}
case "$helper" in /*) ;; *) echo 'absolute helper path required' >&2; exit 2;; esac
test -x "$helper"
exec env YUDESK_NATIVE_TEST_DISPLAY=1 GDK_BACKEND=x11 dbus-run-session -- \
  xvfb-run -a -s '-screen 0 1280x800x24 -nolisten tcp' python3 "$src/test-drag.py" "$helper"
