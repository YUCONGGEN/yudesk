#!/bin/sh
set -eu
arch=${1:-arm64}
out=${2:-}
case "$arch" in arm64) target=arm64;; amd64|x86_64) target=x86_64;; *) echo 'unsupported architecture' >&2; exit 2;; esac
case "$out" in /*) ;; *) echo 'absolute output path required' >&2; exit 2;; esac
src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
sdk=$(xcrun --sdk macosx --show-sdk-path)
xcrun swiftc -swift-version 5 -O -target "$target-apple-macos12.3" -sdk "$sdk" \
  -framework AppKit "$src/Launcher.swift" -o "$out"
