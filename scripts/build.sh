#!/usr/bin/env sh
set -eu

# Keep all release entry points on the same Windows timer-capable toolchain.
export GOTOOLCHAIN="${YUDESK_GO_TOOLCHAIN:-go1.27.1}"

project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
target=${1:-all}
components="yudesk"
obsolete_components="yudesk-account yudesk-admin yudesk-relay yudesk-update"

case "$target" in
  all) targets="windows/amd64 linux/amd64 darwin/amd64 darwin/arm64" ;;
  windows|linux) targets="$target/amd64" ;;
  darwin) targets="darwin/amd64 darwin/arm64" ;;
  *) echo "usage: $0 [all|windows|linux|darwin]" >&2; exit 2 ;;
esac

cd "$project_root"
if command -v sha256sum >/dev/null 2>&1; then
  hash_file() { sha256sum "$1"; }
else
  hash_file() { shasum -a 256 "$1"; }
fi
for platform in $targets; do
  goos=${platform%/*}
  goarch=${platform#*/}
  output_dir="dist/$goos-$goarch"
  mkdir -p "$output_dir"
  obsolete_suffix=""
  [ "$goos" = windows ] && obsolete_suffix=.exe
  for obsolete in $obsolete_components; do
    rm -f "$output_dir/$obsolete$obsolete_suffix"
  done
  for component in $components; do
    suffix=""
    [ "$goos" = windows ] && suffix=.exe
    ldflags='-s -w'
    [ "$goos" = windows ] && ldflags='-s -w -H=windowsgui'
    GOOS=$goos GOARCH=$goarch go build -trimpath -ldflags="$ldflags" -o "$output_dir/$component$suffix" "./cmd/$component"
  done
done

if [ "$target" = all ] || [ "$target" = darwin ]; then
  mkdir -p dist/server
  GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/server/yudesk-relay ./cmd/yudesk-relay
fi

for legacy_name in \
  yudesk-account-darwin-amd64 yudesk-account-linux-amd64 yudesk-account-windows-amd64.exe \
  yudesk-agent-darwin-amd64 yudesk-agent-linux-amd64 yudesk-agent-windows-amd64.exe \
  yudesk-relay-darwin-amd64 yudesk-relay-linux-amd64 yudesk-relay-windows-amd64.exe \
  yudesk-viewer-darwin-amd64 yudesk-viewer-linux-amd64 yudesk-viewer-windows-amd64.exe; do
  rm -f "dist/$legacy_name"
done

: > dist/SHA256SUMS.txt
for platform in $targets; do
  goos=${platform%/*}
  goarch=${platform#*/}
  directory="dist/$goos-$goarch"
  for component in $components; do
    suffix=""
    [ "$goos" = windows ] && suffix=.exe
    hash_file "$directory/$component$suffix"
  done
done | sort -k2 >> dist/SHA256SUMS.txt
echo "Build complete: $project_root/dist"
