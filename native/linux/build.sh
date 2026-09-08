#!/bin/sh
set -eu
# Build on the oldest supported distribution; the output dynamically uses its
# maintained WebKitGTK/GTK packages instead of bundling an unpatched engine.
out=${1:-}
case "$out" in /*) ;; *) echo 'absolute output path required' >&2; exit 2;; esac
src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
pkg-config --atleast-version=2.40 webkit2gtk-4.1
cc -std=c11 -O2 -Wall -Wextra -Werror -Wno-deprecated-declarations \
  -fstack-protector-strong -D_FORTIFY_SOURCE=2 -fPIE -pie -Wl,-z,relro,-z,now \
  "$src/window.c" -o "$out" $(pkg-config --cflags --libs gtk+-3.0 webkit2gtk-4.1 json-glib-1.0)
