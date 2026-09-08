#!/bin/sh
set -eu
# An isolated virtual display ONLY. Do not run these diagnostics against a real
# user's X display, local YuDesk page, or device.
src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
output=${1:-/tmp/yudesk-native-window-tests}
mkdir -p "$output"
sh "$src/build.sh" "$output/yudesk-window"
cc -std=c11 -O1 -g -Wall -Wextra -Werror -Wno-deprecated-declarations -DYUDESK_WINDOW_TEST \
  "$src/window.c" -o "$output/yudesk-window-test" $(pkg-config --cflags --libs gtk+-3.0 webkit2gtk-4.1 json-glib-1.0)
"$output/yudesk-window" --self-test
exec env YUDESK_NATIVE_TEST_DISPLAY=1 dbus-run-session -- xvfb-run -a -s '-screen 0 1280x800x24' \
  python3 "$src/test-window.py" "$output/yudesk-window-test" "$output/yudesk-window" "$output"
