#!/usr/bin/env bash
# Package supplied desktop binaries; never build or run the YuDesk client.
# Compatible with the Bash 3.2 shipped by macOS.
set -euo pipefail
export LC_ALL=C
umask 022

fail() { printf 'package-unix: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "Missing prerequisite: $1"; }
usage() {
  cat <<'HELP'
Usage: bash scripts/package-unix.sh --target macos|linux --arch amd64|arm64
       --staging-dir EXISTING_DIR --output-dir EXISTING_DIR [options]

Input: STAGING_DIR/yudesk and STAGING_DIR/yudesk-window (prebuilt, regular files).
macOS additionally requires STAGING_DIR/yudesk-launcher (the Finder app entry).
Output and input directories must already exist. Existing packages are not replaced.

Common options:
  --version 2.0.0             Must match internal/releaseinfo/release.go
  --package-revision N        Positive integer, default 1; does not change product version
  --licenses-dir DIR         Additional licensing documents (no symlinks/special files)
  --main-sha256 HEX          Expected SHA-256 of the released yudesk binary
  --helper-sha256 HEX        Expected SHA-256 of the released yudesk-window binary
  --keep-work                 Retain the private payload directory for inspection
Linux options (amd64 only):
  --linux-suite SUITE         Required: debian12, debian13, ubuntu22.04, ubuntu24.04
                             Build on that exact distro/release, with its library metadata
  --maintainer 'Name <email>' Required Debian package maintainer
macOS options (run on an actual Mac):
  --macos-min-version X.Y[.Z] Required; checked against all three Mach-O deployment targets
  --launcher-sha256 HEX      Expected SHA-256 of the macOS yudesk-launcher binary
  --app-sign-identity ID      Optional Developer ID Application identity in local keychain
  --installer-sign-identity ID
                             Optional Developer ID Installer identity in local keychain
                             Requires --app-sign-identity; does not notarize
HELP
}

target='' arch='' staging='' output='' linux_suite='' maintainer='' macos_min=''
version=2.0.0 revision=1 extra_licenses='' app_identity='' installer_identity=''
keep_work=0 work=''
expected_main_sha='' expected_helper_sha='' main_input_sha='' helper_input_sha=''
expected_launcher_sha='' launcher_input_sha=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --keep-work) keep_work=1; shift; continue ;;
    --target|--arch|--staging-dir|--output-dir|--version|--package-revision|--linux-suite|--maintainer|--macos-min-version|--licenses-dir|--main-sha256|--helper-sha256|--launcher-sha256|--app-sign-identity|--installer-sign-identity)
      if [ "$#" -lt 2 ] || [ -z "${2:-}" ]; then fail "Missing value for $1"; fi
      case "$2" in --*) fail "Missing value for $1" ;; esac ;;
    *) fail "Unknown option: $1 (use --help)" ;;
  esac
  case "$1" in
    --target) target=$2 ;; --arch) arch=$2 ;;
    --staging-dir) staging=$2 ;; --output-dir) output=$2 ;;
    --version) version=$2 ;; --package-revision) revision=$2 ;;
    --linux-suite) linux_suite=$2 ;; --maintainer) maintainer=$2 ;;
    --macos-min-version) macos_min=$2 ;; --licenses-dir) extra_licenses=$2 ;;
    --main-sha256) expected_main_sha=$2 ;; --helper-sha256) expected_helper_sha=$2 ;;
    --launcher-sha256) expected_launcher_sha=$2 ;;
    --app-sign-identity) app_identity=$2 ;; --installer-sign-identity) installer_identity=$2 ;;
  esac
  shift 2
done

[ "$target" = macos ] || [ "$target" = linux ] || fail '--target must be macos or linux'
[ "$arch" = amd64 ] || [ "$arch" = arm64 ] || fail '--arch must be amd64 or arm64'
[[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || fail 'Invalid product version'
[[ "$revision" =~ ^[1-9][0-9]*$ ]] || fail 'Invalid package revision'
for expected in "$expected_main_sha" "$expected_helper_sha" "$expected_launcher_sha"; do
  [ -z "$expected" ] || [[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] || fail 'Expected SHA-256 must be 64 hex characters'
done
for dir in "$staging" "$output"; do
  [[ -n "$dir" && -d "$dir" ]] || fail '--staging-dir and --output-dir must be existing directories'
done
project_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P)
staging=$(CDPATH='' cd -- "$staging" && pwd -P)
output=$(CDPATH='' cd -- "$output" && pwd -P)
[ "$output" != / ] || fail 'The filesystem root is not an output directory'
[ -w "$output" ] || fail 'Output directory is not writable'
source_version=$(sed -n 's/^const Version = "\([0-9.]*\)".*/\1/p' "$project_root/internal/releaseinfo/release.go")
[ "$version" = "$source_version" ] || fail "Requested version does not match product source ($source_version)"
executables=(yudesk yudesk-window)
if [ "$target" = macos ]; then executables+=(yudesk-launcher); fi
for executable in "${executables[@]}"; do
  [[ -s "$staging/$executable" && -f "$staging/$executable" && ! -L "$staging/$executable" ]] ||
    fail "Missing/empty/non-regular prebuilt binary: $staging/$executable"
done
[ -s "$project_root/third_party/THIRD_PARTY_NOTICES.txt" ] || fail 'Missing third-party notices'
[ -s "$project_root/internal/viewerapp/ui/icon.svg" ] || fail 'Missing Yu icon source'
if [ -n "$extra_licenses" ]; then
  [ -d "$extra_licenses" ] || fail 'Additional license directory does not exist'
  extra_licenses=$(CDPATH='' cd -- "$extra_licenses" && pwd -P)
  [ -z "$(find "$extra_licenses" ! -type d ! -type f -print)" ] || fail 'Additional licenses must contain only directories and regular files'
  # Reject an output tree inside the copy source (including symlink-resolved aliases).
  case "$output/" in "$extra_licenses/"*) fail 'Output must not be inside the additional license directory' ;; esac
fi

cleanup() {
  status=$?
  trap - EXIT
  if [ -n "$work" ]; then
    if [ "$keep_work" = 1 ]; then
      printf 'Inspection directory: %s\n' "$work" >&2
    else
      # Only this invocation's mktemp child of the resolved output directory.
      case "$work" in
        "$output"/.yudesk-package.*)
          if [ -d "$work" ] && [ ! -L "$work" ]; then rm -rf -- "$work"; fi ;;
        *) printf 'Refusing unexpected cleanup path: %s\n' "$work" >&2 ;;
      esac
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

copy_licenses() {
  destination=$1
  mkdir -p "$destination"
  install -m 0644 "$project_root/third_party/THIRD_PARTY_NOTICES.txt" "$destination/THIRD_PARTY_NOTICES.txt"
  # Preserve any first-party licensing documents if the repository acquires them.
  for source in "$project_root"/LICENSE* "$project_root"/COPYING* "$project_root"/NOTICE*; do
    if [ -f "$source" ] && [ ! -L "$source" ]; then install -m 0644 "$source" "$destination/"; fi
  done
  if [ -n "$extra_licenses" ]; then
    mkdir "$destination/additional"
    cp -R "$extra_licenses/." "$destination/additional/"
    find "$destination/additional" -type d -exec chmod 0755 {} +
    find "$destination/additional" -type f -exec chmod 0644 {} +
  fi
}

# Inputs to these templates are validated numeric strings or fixed architecture names.
render() {
  sed -e "s/@VERSION@/$version/g" -e "s/@REVISION@/$revision/g" \
    -e "s/@MACOS_MIN@/$macos_min/g" -e "s/@MACH_ARCH@/${mach_arch:-}/g" "$1" > "$2"
}

write_install_marker() {
  # Same filename/schema; the caller selects the platform's marker directory.
  # Runtime must also validate its
  # canonical executable path and ownership; a copied marker is not installation.
  marker_directory=$1
  marker_platform=$2
  marker_path=$3
  printf '{"schemaVersion":1,"product":"YuDesk","version":"%s","packageRevision":"%s","platform":"%s","architecture":"%s","installed":true,"executable":"%s/yudesk","helper":"%s/yudesk-window"}\n' \
    "$version" "$revision" "$marker_platform" "$arch" "$marker_path" "$marker_path" > "$marker_directory/yudesk-install.json"
  chmod 0644 "$marker_directory/yudesk-install.json"
}

hash_file() {
  if [ "$target" = linux ]; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'
  fi
}

record_inputs() {
  main_input_sha=$(hash_file "$staging/yudesk")
  helper_input_sha=$(hash_file "$staging/yudesk-window")
  if [ -n "$expected_main_sha" ]; then
    [ "$main_input_sha" = "$(printf '%s' "$expected_main_sha" | tr '[:upper:]' '[:lower:]')" ] || fail 'Main SHA-256 mismatch; do not package an old or changing Go build'
  fi
  if [ -n "$expected_helper_sha" ]; then
    [ "$helper_input_sha" = "$(printf '%s' "$expected_helper_sha" | tr '[:upper:]' '[:lower:]')" ] || fail 'Helper SHA-256 mismatch; wait for the verified native helper'
  fi
  printf 'Input yudesk SHA-256: %s\nInput yudesk-window SHA-256: %s\n' "$main_input_sha" "$helper_input_sha"
  if [ "$target" = macos ]; then
    launcher_input_sha=$(hash_file "$staging/yudesk-launcher")
    if [ -n "$expected_launcher_sha" ]; then
      [ "$launcher_input_sha" = "$(printf '%s' "$expected_launcher_sha" | tr '[:upper:]' '[:lower:]')" ] || fail 'Launcher SHA-256 mismatch; wait for the verified native launcher'
    fi
    printf 'Input yudesk-launcher SHA-256: %s\n' "$launcher_input_sha"
  fi
}

verify_copies() {
  [ "$(hash_file "$1/yudesk")" = "$main_input_sha" ] || fail 'Copied Go binary differs from the checked input'
  [ "$(hash_file "$1/yudesk-window")" = "$helper_input_sha" ] || fail 'Copied helper differs from the checked input'
  printf '%s  yudesk\n%s  yudesk-window\n' "$main_input_sha" "$helper_input_sha" > "$work/input-sha256.txt"
  if [ "$target" = macos ]; then
    [ "$(hash_file "$1/yudesk-launcher")" = "$launcher_input_sha" ] || fail 'Copied launcher differs from the checked input'
    printf '%s  yudesk-launcher\n' "$launcher_input_sha" >> "$work/input-sha256.txt"
  fi
}

verify_inputs_unchanged() {
  [ "$(hash_file "$staging/yudesk")" = "$main_input_sha" ] || fail 'Go input changed during packaging; package was not published'
  [ "$(hash_file "$staging/yudesk-window")" = "$helper_input_sha" ] || fail 'Helper input changed during packaging; package was not published'
  if [ "$target" = macos ]; then
    [ "$(hash_file "$staging/yudesk-launcher")" = "$launcher_input_sha" ] || fail 'Launcher input changed during packaging; package was not published'
  fi
}

if [ "$target" = linux ]; then
  [ "$(uname -s)" = Linux ] || fail 'Build .deb on the selected Linux release (a matching container is supported)'
  [ "$arch" = amd64 ] || fail 'Linux installer currently supports amd64 only'
  [ -z "$macos_min$app_identity$installer_identity$expected_launcher_sha" ] || fail 'macOS options cannot be used for Linux'
  case "$linux_suite" in
    debian12) distro=debian; distro_version=12; gtk_package=libgtk-3-0 ;;
    debian13) distro=debian; distro_version=13; gtk_package=libgtk-3-0t64 ;;
    ubuntu22.04) distro=ubuntu; distro_version=22.04; gtk_package=libgtk-3-0 ;;
    ubuntu24.04) distro=ubuntu; distro_version=24.04; gtk_package=libgtk-3-0t64 ;;
    *) fail 'Specify --linux-suite debian12|debian13|ubuntu22.04|ubuntu24.04' ;;
  esac
  [ -r /etc/os-release ] || fail 'Cannot identify Linux build host'
  # os-release is host-owned metadata, not supplied staging input.
  # shellcheck disable=SC1091
  . /etc/os-release
  [[ "${ID:-}" = "$distro" && "${VERSION_ID:-}" = "$distro_version" ]] ||
    fail "Build --linux-suite $linux_suite on $distro $distro_version; found ${ID:-unknown} ${VERSION_ID:-unknown}"
  [[ "$maintainer" =~ ^[^\<\>]+\ \<[^\ \<\>]+@[^\ \<\>]+\>$ ]] || fail '--maintainer must be Name <email>'
  case "$maintainer" in *$'\n'*|*$'\r'*) fail 'Invalid maintainer field' ;; esac
  for command in dpkg-deb dpkg-shlibdeps dpkg-architecture readelf desktop-file-validate sha256sum; do need "$command"; done
  record_inputs
  [ "$(dpkg-architecture -qDEB_HOST_ARCH)" = amd64 ] || fail 'Use an amd64 Linux build host/container'
  for executable in yudesk yudesk-window; do
    header=$(readelf -h "$staging/$executable") || fail "Not an ELF binary: $executable"
    grep -Eq 'Class:[[:space:]]+ELF64' <<< "$header" || fail "$executable is not ELF64"
    grep -Eq 'Machine:[[:space:]]+Advanced Micro Devices X86-64' <<< "$header" || fail "$executable is not amd64"
    grep -Eq 'Type:[[:space:]]+(EXEC|DYN)' <<< "$header" || fail "$executable is not executable ELF"
    dynamic=$(readelf -d "$staging/$executable")
    if grep -Eq '\((RPATH|RUNPATH)\)' <<< "$dynamic"; then
      fail "$executable contains RPATH/RUNPATH; supply binaries linked against the target system libraries"
    fi
  done
  helper_links=$(readelf -d "$staging/yudesk-window")
  for library in libgtk-3.so.0 libwebkit2gtk-4.1.so.0; do
    grep -Fq "Shared library: [$library]" <<< "$helper_links" || fail "yudesk-window must link $library"
  done
  name="YuDesk-$version-$revision-linux-amd64-$linux_suite.deb"
  [[ ! -e "$output/$name" && ! -L "$output/$name" ]] || fail "Output already exists: $output/$name"
  work=$(mktemp -d "$output/.yudesk-package.XXXXXXXX")
  root="$work/root"
  mkdir -p "$root/DEBIAN" "$root/usr/lib/yudesk" "$root/usr/bin" \
    "$root/usr/share/applications" "$root/usr/share/icons/hicolor/scalable/apps" "$work/debian"
  for executable in yudesk yudesk-window; do
    install -m 0755 "$staging/$executable" "$root/usr/lib/yudesk/$executable"
  done
  verify_copies "$root/usr/lib/yudesk"
  write_install_marker "$root/usr/lib/yudesk" linux /usr/lib/yudesk
  ln -s ../lib/yudesk/yudesk "$root/usr/bin/yudesk"
  install -m 0644 "$project_root/packaging/linux/yudesk.desktop" "$root/usr/share/applications/yudesk.desktop"
  install -m 0644 "$project_root/internal/viewerapp/ui/icon.svg" "$root/usr/share/icons/hicolor/scalable/apps/yudesk.svg"
  desktop-file-validate "$root/usr/share/applications/yudesk.desktop"
  copy_licenses "$root/usr/share/doc/yudesk"
  install -m 0644 "$project_root/packaging/linux/copyright" "$root/usr/share/doc/yudesk/copyright"
  # dpkg-shlibdeps requires source metadata even when packaging prebuilt executables.
  printf 'Source: yudesk\nSection: net\nPriority: optional\nMaintainer: %s\n\nPackage: yudesk\nArchitecture: amd64\nDescription: YuDesk desktop client\n' "$maintainer" > "$work/debian/control"
  dependency_args=()
  for executable in yudesk yudesk-window; do
    # Consume readelf's full output: grep -q can SIGPIPE the producer under pipefail.
    if readelf -d "$root/usr/lib/yudesk/$executable" | grep -F '(NEEDED)' >/dev/null; then
      dependency_args+=("-e$root/usr/lib/yudesk/$executable")
    fi
  done
  # Never execute staged binaries (including ldd). Do not ignore missing libraries/symbols.
  if ! dependencies=$(cd "$work" && dpkg-shlibdeps -O --warnings=1 "${dependency_args[@]}" 2> "$work/dependency-warnings.txt"); then
    cat "$work/dependency-warnings.txt" >&2
    fail 'Dependency analysis failed; install dpkg-dev and the matching target libraries/development packages'
  fi
  if [ -s "$work/dependency-warnings.txt" ]; then
    cat "$work/dependency-warnings.txt" >&2
    fail 'Dependency analysis reported unresolved symbols/warnings; use binaries built for this target release'
  fi
  dependencies=${dependencies#shlibs:Depends=}
  case "$dependencies" in *$'\n'*) fail 'Unexpected multiline dependency metadata' ;; esac
  [ -n "$dependencies" ] || fail 'No shared-library dependencies were discovered'
  installed_size=$(du -sk "$root/usr" | awk '{print $1}')
  {
    printf 'Package: yudesk\nVersion: %s-%s\nArchitecture: amd64\n' "$version" "$revision"
    printf 'Maintainer: %s\nSection: net\nPriority: optional\nInstalled-Size: %s\n' "$maintainer" "$installed_size"
    printf 'Depends: %s, %s (>= 3.24), libwebkit2gtk-4.1-0 (>= 2.40), hicolor-icon-theme, xdotool, maim | imagemagick | gnome-screenshot, xclip | xsel\n' "$dependencies" "$gtk_package"
    printf 'Recommends: fonts-noto-cjk\n'
    printf 'Description: YuDesk installed desktop client\n Native desktop window for attended remote desktop sessions.\n Launch YuDesk from the desktop application menu.\n'
  } > "$root/DEBIAN/control"
  chmod 0755 "$root" "$root/DEBIAN"
  (cd "$root" && find usr -type f -exec md5sum {} + > DEBIAN/md5sums)
  dpkg-deb --build --root-owner-group -Zxz "$root" "$work/$name"
  dpkg-deb --info "$work/$name" >/dev/null
  verify_inputs_unchanged
  # Atomic no-clobber publication on the same filesystem, even with parallel builders.
  ln "$work/$name" "$output/$name" || fail "Cannot publish without overwriting: $output/$name"
  (cd "$output" && sha256sum "$name")
else
  [ "$(uname -s)" = Darwin ] || fail 'macOS .pkg and .icns generation require an actual Mac; no cross-host iconutil substitute is used'
  [ -z "$linux_suite$maintainer" ] || fail 'Linux options cannot be used for macOS'
  [[ "$macos_min" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || fail 'Specify --macos-min-version X.Y[.Z]'
  [ -z "$installer_identity" ] || [ -n "$app_identity" ] || fail 'Installer signing also requires --app-sign-identity'
  for command in xcrun pkgbuild productbuild plutil lipo otool iconutil shasum stat codesign; do need "$command"; done
  record_inputs
  xcrun --find clang >/dev/null || fail 'Xcode Command Line Tools with clang are required'
  case "$arch" in amd64) mach_arch=x86_64 ;; arm64) mach_arch=arm64 ;; esac
  version_at_most() {
    awk -v left="$1" -v right="$2" 'BEGIN {split(left,a,"."); split(right,b,"."); for(i=1;i<=3;i++){if(a[i]+0<b[i]+0)exit 0; if(a[i]+0>b[i]+0)exit 1} exit 0}'
  }
  for executable in "${executables[@]}"; do
    binary_arch=$(lipo -archs "$staging/$executable") || fail "Not Mach-O: $executable"
    [ "$binary_arch" = "$mach_arch" ] || fail "$executable must be a thin $mach_arch binary (found: $binary_arch)"
    minimum=$(otool -l "$staging/$executable" | awk '$1=="cmd" {legacy=($2=="LC_VERSION_MIN_MACOSX"); build=($2=="LC_BUILD_VERSION")} (legacy && $1=="version") || (build && $1=="minos") {print $2}')
    [[ "$minimum" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || fail "Cannot read deployment target for $executable"
    version_at_most "$minimum" "$macos_min" || fail "$executable requires macOS $minimum, above declared $macos_min"
    rpaths=$(otool -l "$staging/$executable" | awk '$1=="cmd" {rpath=($2=="LC_RPATH")} rpath && $1=="path" {print $2}')
    links=$(otool -L "$staging/$executable" | tail -n +2 | awk '{print $1}')
    while IFS= read -r library; do
      case "$library" in
        /System/Library/*|/usr/lib/*|'') ;;
        @rpath/libswift*.dylib)
          # swiftc normally references OS Swift libraries this way. macOS ships
          # them in its shared cache, so a regular-file existence test is wrong.
          [ "${rpaths%%$'\n'*}" = /usr/lib/swift ] || fail "$executable must resolve Swift runtime from /usr/lib/swift first"
          version_at_most 12.3 "$macos_min" || fail 'System Swift helper requires a declared macOS minimum of at least 12.3' ;;
        *) fail "Unbundled non-system library in $executable: $library" ;;
      esac
    done <<< "$links"
  done
  helper_links=$(otool -L "$staging/yudesk-window")
  for framework in AppKit WebKit; do
    grep -Fq "/$framework.framework/" <<< "$helper_links" || fail "yudesk-window must link $framework.framework"
  done
  launcher_links=$(otool -L "$staging/yudesk-launcher")
  grep -Fq '/AppKit.framework/' <<< "$launcher_links" || fail 'yudesk-launcher must link AppKit.framework'
  name="YuDesk-$version-$revision-macos-$arch.pkg"
  [[ ! -e "$output/$name" && ! -L "$output/$name" ]] || fail "Output already exists: $output/$name"
  work=$(mktemp -d "$output/.yudesk-package.XXXXXXXX")
  app="$work/root/Applications/YuDesk.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" "$work/YuDesk.iconset" "$work/packages"
  for executable in "${executables[@]}"; do
    # Install copies without modifying the supplied binaries or their signatures.
    install -m 0755 "$staging/$executable" "$app/Contents/MacOS/$executable"
  done
  verify_copies "$app/Contents/MacOS"
  # Data belongs in Resources; MacOS entries are treated as nested executable code.
  write_install_marker "$app/Contents/Resources" darwin /Applications/YuDesk.app/Contents/MacOS
  render "$project_root/packaging/macos/Info.plist.in" "$app/Contents/Info.plist"
  plutil -lint "$app/Contents/Info.plist"
  printf 'APPL????' > "$app/Contents/PkgInfo"
  copy_licenses "$app/Contents/Resources/licenses"
  # Build this small asset renderer for the packaging host, independently of app architecture.
  xcrun clang -fobjc-arc -Wall -Wextra -Werror -framework AppKit -framework CoreGraphics \
    "$project_root/packaging/macos/render-icon.m" -o "$work/render-icon"
  "$work/render-icon" "$work/YuDesk.iconset"
  iconutil --convert icns --output "$app/Contents/Resources/YuDesk.icns" "$work/YuDesk.iconset"
  # Sign nested code first, then seal the completed bundle, including its launcher.
  # Bare executable signatures are not a resource seal for CFBundleExecutable.
  if [ -n "$app_identity" ]; then
    codesign_args=(--force --options runtime --timestamp --sign "$app_identity")
  else
    codesign_args=(--force --sign - --timestamp=none)
    printf 'WARNING: App uses local ad-hoc signatures only, not Developer ID or notarization; installer remains unsigned.\n' >&2
  fi
  for executable in "${executables[@]}"; do
    codesign "${codesign_args[@]}" "$app/Contents/MacOS/$executable"
  done
  codesign "${codesign_args[@]}" "$app"
  for executable in "${executables[@]}"; do
    codesign --verify --strict --verbose=2 "$app/Contents/MacOS/$executable"
  done
  codesign --verify --strict --deep --verbose=2 "$app"
  # Signing changes copied Mach-O bytes, never the pinned staging inputs.
  : > "$work/packaged-sha256.txt"
  for executable in "${executables[@]}"; do
    [ "$(stat -f '%Lp' "$app/Contents/MacOS/$executable")" = 755 ] || fail "Unexpected executable permissions: $executable"
    printf '%s  %s\n' "$(hash_file "$app/Contents/MacOS/$executable")" "$executable" >> "$work/packaged-sha256.txt"
  done
  [ "$(stat -f '%Lp' "$app/Contents/Resources/yudesk-install.json")" = 644 ] || fail 'Unexpected install marker permissions'
  pkgbuild --root "$work/root" --install-location / --ownership recommended \
    --component-plist "$project_root/packaging/macos/components.plist" \
    --identifier com.yudesk.desktop --version "$version.$revision" "$work/packages/YuDesk-component.pkg"
  render "$project_root/packaging/macos/Distribution.xml.in" "$work/Distribution.xml"
  productbuild_args=(--distribution "$work/Distribution.xml" --package-path "$work/packages"
    --resources "$project_root/packaging/macos/resources")
  if [ -n "$installer_identity" ]; then productbuild_args+=(--sign "$installer_identity" --timestamp); fi
  productbuild "${productbuild_args[@]}" "$work/$name"
  verify_inputs_unchanged
  ln "$work/$name" "$output/$name" || fail "Cannot publish without overwriting: $output/$name"
  (cd "$output" && shasum -a 256 "$name")
  if [ -z "$installer_identity" ]; then printf 'WARNING: Installer is unsigned.\n' >&2; fi
  printf 'NOT NOTARIZED: Gatekeeper acceptance and GUI installation on a clean Mac remain release checks.\n' >&2
  printf 'INPUT LIMITATION: cliclick is not bundled. Receiving remote input needs a compatible separately provisioned cliclick on the GUI process PATH and macOS Accessibility permission.\n' >&2
fi
printf 'Created: %s/%s\n' "$output" "$name"
