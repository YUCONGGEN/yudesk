#!/usr/bin/env bash
# Non-root packaging/negative-path tests; run in an isolated matching Linux image.
set -euo pipefail
export LC_ALL=C
project_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P)
for command in gcc pkg-config python3 dpkg-deb apt-get shellcheck desktop-file-validate; do
  command -v "$command" >/dev/null || { printf 'Missing test prerequisite: %s\n' "$command" >&2; exit 1; }
done
# shellcheck disable=SC1091
. /etc/os-release
case "$ID:$VERSION_ID" in
  debian:12) suite=debian12 ;; debian:13) suite=debian13 ;;
  ubuntu:22.04) suite=ubuntu22.04 ;; ubuntu:24.04) suite=ubuntu24.04 ;;
  *) printf 'Unsupported test host\n' >&2; exit 1 ;;
esac
bash -n "$project_root/scripts/package-unix.sh"
shellcheck "$project_root/scripts/package-unix.sh" "$project_root/scripts/package-unix.test.sh"
python3 "$project_root/packaging/macos/tests/test-signing-order.py"
test_root=$(mktemp -d /tmp/yudesk-package-test.XXXXXXXX)
cleanup() {
  case "$test_root" in /tmp/yudesk-package-test.*) [ ! -L "$test_root" ] && rm -rf -- "$test_root" ;; esac
}
trap cleanup EXIT
mkdir -p "$test_root/staging binaries" "$test_root/output packages" "$test_root/licenses supplied/nested" "$test_root/user state"
staging="$test_root/staging binaries"
output="$test_root/output packages"
gcc "$project_root/packaging/linux/tests/fixture-main.c" -o "$staging/yudesk"
# shellcheck disable=SC2046
gcc "$project_root/packaging/linux/tests/fixture-window.c" -o "$staging/yudesk-window" $(pkg-config --cflags --libs gtk+-3.0 webkit2gtk-4.1)
cp "$project_root/packaging/linux/copyright" "$test_root/licenses supplied/nested/fixture notice.txt"
cp "$project_root/packaging/linux/copyright" "$test_root/user state/keep.txt"
before=$(sha256sum "$staging/yudesk" "$staging/yudesk-window" "$test_root/user state/keep.txt")
common=(--target linux --arch amd64 --staging-dir "$staging" --output-dir "$output"
  --linux-suite "$suite" --maintainer 'Packaging Test <test@example.invalid>')
bash "$project_root/scripts/package-unix.sh" "${common[@]}" --keep-work --licenses-dir "$test_root/licenses supplied" \
  --main-sha256 "$(sha256sum "$staging/yudesk" | awk '{print $1}')" \
  --helper-sha256 "$(sha256sum "$staging/yudesk-window" | awk '{print $1}')"
package="$output/YuDesk-2.0.0-1-linux-amd64-$suite.deb"
dpkg-deb --extract "$package" "$test_root/extracted"
dpkg-deb --control "$package" "$test_root/control"
desktop-file-validate "$test_root/extracted/usr/share/applications/yudesk.desktop"
cmp "$staging/yudesk" "$test_root/extracted/usr/lib/yudesk/yudesk"
cmp "$staging/yudesk-window" "$test_root/extracted/usr/lib/yudesk/yudesk-window"
cmp "$project_root/internal/viewerapp/ui/icon.svg" "$test_root/extracted/usr/share/icons/hicolor/scalable/apps/yudesk.svg"
cmp "$project_root/third_party/THIRD_PARTY_NOTICES.txt" "$test_root/extracted/usr/share/doc/yudesk/THIRD_PARTY_NOTICES.txt"
cmp "$test_root/licenses supplied/nested/fixture notice.txt" "$test_root/extracted/usr/share/doc/yudesk/additional/nested/fixture notice.txt"
(cd "$test_root/extracted" && md5sum --check "$test_root/control/md5sums")
[ "$before" = "$(sha256sum "$staging/yudesk" "$staging/yudesk-window" "$test_root/user state/keep.txt")" ]
python3 "$project_root/packaging/linux/tests/inspect-package.py" "$package" "$project_root"
# Resolve the complete GUI/input/capture/clipboard/font dependency graph, without
# installing the package, invoking any package hooks, or launching a client.
[ "$(dpkg-deb --field "$package" Recommends)" = fonts-noto-cjk ]
apt-get --simulate install "$package" > "$test_root/apt-simulation.log"
if ! dpkg-query -W -f='${Status}' fonts-noto-cjk 2>/dev/null | grep -Fx 'install ok installed' >/dev/null; then
  grep -Eq '^Inst fonts-noto-cjk([ :])' "$test_root/apt-simulation.log"
fi
printf 'APT dependency simulation passed (default recommendations, CJK fonts selected or already installed): %s\n' "$suite"

expect_failure() {
  expected=$1
  shift
  if bash "$project_root/scripts/package-unix.sh" "$@" > "$test_root/failure.log" 2>&1; then
    printf 'Unexpected success (expected: %s)\n' "$expected" >&2; exit 1
  fi
  grep -Fq -- "$expected" "$test_root/failure.log" || { cat "$test_root/failure.log"; exit 1; }
}
package_hash=$(sha256sum "$package")
expect_failure 'Output already exists' "${common[@]}"
[ "$package_hash" = "$(sha256sum "$package")" ]
expect_failure 'does not match product source' "${common[@]}" --version 9.9.9
expect_failure 'amd64 only' "${common[@]}" --arch arm64
expect_failure 'must be existing directories' "${common[@]}" --output-dir "$test_root/missing"
expect_failure 'must be Name <email>' "${common[@]}" --maintainer $'Bad\nInjected: field'
expect_failure 'Missing/empty/non-regular prebuilt binary:' --target macos --arch arm64 --staging-dir "$staging" --output-dir "$output" --macos-min-version 13.0
# Only to exercise the non-Mac host guard; never run or package this placeholder.
cp "$staging/yudesk" "$staging/yudesk-launcher"
expect_failure 'require an actual Mac' --target macos --arch arm64 --staging-dir "$staging" --output-dir "$output" --macos-min-version 13.0
expect_failure 'macOS options cannot be used for Linux' "${common[@]}" --launcher-sha256 0000000000000000000000000000000000000000000000000000000000000000
expect_failure 'Missing value' --target
expect_failure 'Unknown option' --unknown
expect_failure 'Main SHA-256 mismatch' "${common[@]}" --main-sha256 0000000000000000000000000000000000000000000000000000000000000000
expect_failure 'Helper SHA-256 mismatch' "${common[@]}" --helper-sha256 0000000000000000000000000000000000000000000000000000000000000000
expect_failure '64 hex characters' "${common[@]}" --main-sha256 invalid
expect_failure '64 hex characters' "${common[@]}" --launcher-sha256 invalid
mkdir "$test_root/missing helper"
cp "$staging/yudesk" "$test_root/missing helper/yudesk"
expect_failure 'Missing/empty/non-regular' "${common[@]}" --staging-dir "$test_root/missing helper"
ln -s "$staging/yudesk-window" "$test_root/missing helper/yudesk-window"
expect_failure 'Missing/empty/non-regular' "${common[@]}" --staging-dir "$test_root/missing helper"
mkdir "$test_root/wrong helper"
cp "$staging/yudesk" "$test_root/wrong helper/yudesk"
cp "$staging/yudesk" "$test_root/wrong helper/yudesk-window"
expect_failure 'must link libgtk-3.so.0' "${common[@]}" --staging-dir "$test_root/wrong helper"
mkdir "$test_root/bad licenses"
ln -s "$staging/yudesk" "$test_root/bad licenses/link"
expect_failure 'only directories and regular files' "${common[@]}" --licenses-dir "$test_root/bad licenses"
expect_failure 'Output must not be inside' "${common[@]}" --licenses-dir "$test_root/licenses supplied" --output-dir "$test_root/licenses supplied/nested"
wrong_suite=debian12
[ "$suite" != debian12 ] || wrong_suite=debian13
expect_failure 'Build --linux-suite' "${common[@]}" --linux-suite "$wrong_suite"

# Packaging a static Go-style main must still analyze the native helper's ABI.
mkdir "$test_root/static main"
gcc -static "$project_root/packaging/linux/tests/fixture-main.c" -o "$test_root/static main/yudesk"
cp "$staging/yudesk-window" "$test_root/static main/yudesk-window"
bash "$project_root/scripts/package-unix.sh" "${common[@]}" --staging-dir "$test_root/static main" --package-revision 2
dpkg-deb --extract "$output/YuDesk-2.0.0-2-linux-amd64-$suite.deb" "$test_root/static extracted"
cmp "$test_root/static main/yudesk" "$test_root/static extracted/usr/lib/yudesk/yudesk"
python3 "$project_root/packaging/linux/tests/inspect-package.py" "$output/YuDesk-2.0.0-2-linux-amd64-$suite.deb" "$project_root"

# An unavailable packaging tool must fail honestly, before publishing anything.
mkdir "$test_root/missing tools"
for command in dirname sed uname; do ln -s "$(command -v "$command")" "$test_root/missing tools/$command"; done
if PATH="$test_root/missing tools" "$BASH" "$project_root/scripts/package-unix.sh" "${common[@]}" --package-revision 3 > "$test_root/failure.log" 2>&1; then
  printf 'Unexpected success without dpkg-deb\n' >&2; exit 1
fi
grep -Fq 'Missing prerequisite: dpkg-deb' "$test_root/failure.log"
[ ! -e "$output/YuDesk-2.0.0-3-linux-amd64-$suite.deb" ]
printf 'PASS: %s non-root archive, dependencies, marker, notices, launcher, input preservation and failure paths\n' "$suite"
