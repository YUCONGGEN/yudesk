#!/bin/sh
# Build and verify both thin macOS client packages on the trusted macOS host.
set -eu
export LC_ALL=C
umask 077

root=/Users/yu/bin/yudesk
name=${1:?release name required}
revision=${2:?package revision required}
build_commit=${3:?build commit required}
source_hash=${4:?source archive SHA-256 required}
amd64_hash=${5:?amd64 main SHA-256 required}
arm64_hash=${6:?arm64 main SHA-256 required}
case "$name" in ''|*[!a-zA-Z0-9._-]*|.*) exit 2;; esac
case "$revision" in ''|*[!0-9]*) exit 2;; esac
test "$revision" -gt 0
case "$build_commit" in *[!0-9a-f]*) exit 2;; esac
test "${#build_commit}" = 40
for value in "$source_hash" "$amd64_hash" "$arm64_hash"; do
  case "$value" in *[!0-9a-f]*) exit 2;; esac
  test "${#value}" = 64
done

parent="$root/staging"
archive="$parent/$name-macos-source.tgz"
build="$parent/$name-macos-build"
test -s "$archive" && test ! -L "$archive"
test "$(shasum -a 256 "$archive" | awk '{print $1}')" = "$source_hash"
test ! -e "$build" && test ! -L "$build"
for arch in amd64 arm64; do
  binary="$parent/$name-yudesk-$arch"
  test -s "$binary" && test ! -L "$binary"
done
test "$(shasum -a 256 "$parent/$name-yudesk-amd64" | awk '{print $1}')" = "$amd64_hash"
test "$(shasum -a 256 "$parent/$name-yudesk-arm64" | awk '{print $1}')" = "$arm64_hash"

mkdir "$build" "$build/source" "$build/inputs" "$build/output" "$build/tmp"
tar -xzf "$archive" -C "$build/source"
test -z "$(find "$build/source" -type l -print)"

for arch in amd64 arm64; do
  input="$build/inputs/darwin-$arch"
  mkdir "$input"
  cp "$parent/$name-yudesk-$arch" "$input/yudesk"
  sh "$build/source/native/macos/build.sh" "$arch" "$input/yudesk-window"
  sh "$build/source/native/macos/build-launcher.sh" "$arch" "$input/yudesk-launcher"
  chmod 755 "$input/yudesk" "$input/yudesk-window" "$input/yudesk-launcher"
done

"$build/inputs/darwin-arm64/yudesk-window" --self-test | grep -F 'self_test_passed' >/dev/null
for arch in amd64 arm64; do
  input="$build/inputs/darwin-$arch"
  main_sha=$(shasum -a 256 "$input/yudesk" | awk '{print $1}')
  helper_sha=$(shasum -a 256 "$input/yudesk-window" | awk '{print $1}')
  launcher_sha=$(shasum -a 256 "$input/yudesk-launcher" | awk '{print $1}')
  (cd "$build/source" && env TMPDIR="$build/tmp" bash scripts/package-unix.sh \
    --target macos --arch "$arch" --version 2.0.0 --package-revision "$revision" \
    --build-commit "$build_commit" \
    --macos-min-version 13.0 --staging-dir "$input" --output-dir "$build/output" \
    --main-sha256 "$main_sha" --helper-sha256 "$helper_sha" --launcher-sha256 "$launcher_sha")
  package="$build/output/YuDesk-2.0.0-$revision-macos-$arch.pkg"
  inspect="$build/inspect-$arch"
  pkgutil --expand-full "$package" "$inspect"
  app="$inspect/YuDesk-component.pkg/Payload/Applications/YuDesk.app"
  plutil -lint "$app/Contents/Info.plist" >/dev/null
  test "$(plutil -extract YuDeskPackageRevision raw "$app/Contents/Info.plist")" = "$revision"
  codesign --verify --strict --deep --verbose=2 "$app"
  machine=x86_64
  test "$arch" = arm64 && machine=arm64
  for executable in yudesk yudesk-window yudesk-launcher; do
    test "$(lipo -archs "$app/Contents/MacOS/$executable")" = "$machine"
    codesign --verify --strict --verbose=2 "$app/Contents/MacOS/$executable"
  done
  shasum -a 256 "$package"
done
