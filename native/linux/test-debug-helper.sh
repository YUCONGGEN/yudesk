#!/bin/sh
# Test-only debugger for our own synthetic Xvfb fixture; never shipped.
set -eu
test "${YUDESK_NATIVE_TEST_DISPLAY:-}" = 1
test -n "${DISPLAY:-}"
test -n "${YUDESK_NATIVE_DEBUG_BINARY:-}"
test -n "${YUDESK_NATIVE_DEBUG_LOG:-}"
ulimit -c 0
exec gdb -q -batch -ex 'set pagination off' -ex 'set confirm off' \
  -ex 'set debuginfod enabled off' -ex 'set startup-with-shell off' \
  -ex "set logging file $YUDESK_NATIVE_DEBUG_LOG" \
  -ex 'set logging redirect on' -ex 'set logging enabled on' \
  -ex run -ex 'thread apply all bt' -ex 'info proc mappings' \
  -ex 'frame 3' -ex 'x/12i $pc-24' -ex 'frame 4' -ex 'x/30i $pc-60' -ex kill \
  --args "$YUDESK_NATIVE_DEBUG_BINARY"
