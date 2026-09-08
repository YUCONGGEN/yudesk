#!/bin/sh
set -eu
# Takes an ALREADY BUILT diagnostic helper. Never rebuilds any production binary.
src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
helper=${1:?existing test helper required}
output=${2:?absolute output directory required}
case "$helper" in /*) ;; *) echo 'absolute test helper path required' >&2; exit 2;; esac
case "$output" in /*) ;; *) echo 'absolute output path required' >&2; exit 2;; esac
test -x "$helper"
exec env YUDESK_NATIVE_TEST_DISPLAY=1 dbus-run-session -- xvfb-run -a -s '-screen 0 1280x800x24' \
  python3 "$src/test-dashboard.py" "$helper" "$output"
