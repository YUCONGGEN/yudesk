# YuDesk Unix desktop installers

`scripts/package-unix.sh` packages **supplied prebuilt** product `2.0.0`
executables. It does not compile, run, stop, or contact a YuDesk client or server.
It does not deploy anything. Normal users install through the operating system's
graphical installer and launch **YuDesk** from Applications/the desktop menu.

Both platforms require staging files `yudesk` (the Go core) and
`yudesk-window` (the native window helper). macOS additionally requires
`yudesk-launcher`, the native AppKit application entry point. The main integration owns the core/helper JSON
stdin/stdout protocol and frameless behavior. The package always includes the
helper and never substitutes a browser. Helper sources in `native/macos` and
`native/linux` are built separately; successful packaging alone does not prove
native window, permission, or remote-session behavior.

## Installed locations and marker contract

| Platform | Main executable | Sibling executables | Install marker |
| --- | --- | --- | --- |
| macOS Intel / Apple silicon | `/Applications/YuDesk.app/Contents/MacOS/yudesk` | `yudesk-window`, `yudesk-launcher` | `/Applications/YuDesk.app/Contents/Resources/yudesk-install.json` |
| Debian / Ubuntu amd64 | `/usr/lib/yudesk/yudesk` | `yudesk-window` | `/usr/lib/yudesk/yudesk-install.json` |

All packaged executables have mode `0755`; the marker has mode `0644`. Debian archive
ownership is `root:root` even when built without root. macOS `pkgbuild` uses
recommended system ownership. Neither executable nor marker is group/world
writable. The Linux `/usr/bin/yudesk` link points to `../lib/yudesk/yudesk`.
The desktop menu entry directly executes `/usr/lib/yudesk/yudesk`, without a
terminal or elevated privileges.

On macOS, `CFBundleExecutable=yudesk-launcher` and `LSUIElement=true`. Finder
starts this AppKit event loop, which launches the sibling Go core and handles
Finder reopen events through the core's existing single-instance protocol.
The launcher has no additional Dock icon; the window helper owns the visible
application window. This is an application entry process, not a launchd service
or login item. Its source and behavioral acceptance belong to the native agent.

The marker is UTF-8 JSON with this schema (macOS uses `platform: "darwin"`, the
chosen architecture, and its canonical paths):

```json
{"schemaVersion":1,"product":"YuDesk","version":"2.0.0","packageRevision":"1","platform":"linux","architecture":"amd64","installed":true,"executable":"/usr/lib/yudesk/yudesk","helper":"/usr/lib/yudesk/yudesk-window"}
```

The Go installed-mode query should resolve its actual executable with
`os.Executable` and `filepath.EvalSymlinks`, compare against the canonical
platform path above, then read the platform-specific marker and validate the expected
schema/platform/paths. Validate the helper's canonical sibling path and
ownership/modes too. A marker in a downloaded directory, an extracted test root,
or a renamed/copied app must not by itself classify the running copy as installed.
Do not infer installation from the working directory, `$PATH`, or a marker in
the user's state directory. This document specifies the contract; the Go query
implementation is outside the packaging scope.

On macOS, derive the marker from the canonical executable directory as
`../Resources/yudesk-install.json`, then validate its canonical bundle location,
root ownership and non-group/world-writable mode. Linux keeps the sibling marker.
The macOS marker is sealed as bundle data in `Contents/Resources`, not placed
in `Contents/MacOS`, where codesign treats entries as nested executable code.
There is no legacy MacOS-marker fallback or resource-rule exclusion.

The JSON schema remains unchanged: `executable` still names the **Go core**
`.../Contents/MacOS/yudesk`, not the Finder launcher. No new launcher field is
added, because the Go parser rejects unknown fields. Both JSON paths still name
executables in `Contents/MacOS`, never in Resources. The launcher is packaged
in the same protected directory with the same executable ownership/mode policy.

## End-user workflow and state

macOS users open the matching Intel or Apple silicon `.pkg`, follow Installer,
then open **Applications → YuDesk**. The installer includes English/Chinese
welcome and completion pages. Users remove the application using Finder/Trash
after quitting it.

Linux users open the `.deb` matching their distribution release in its graphical
package installer, approve installation/dependencies, then open **YuDesk** in
the application menu. Removal uses that package manager's graphical interface.
A desktop session and a GUI installer that supports local `.deb` files must be
present; a minimal/server distribution is not a supported no-terminal setup.
Enabling a missing repository or provisioning the desktop is an administrator's
task, not an end-user command-line installation flow.

The Linux package also declares the existing desktop backend's **external tools**
as required dependencies: `xdotool` for X11 mouse/keyboard input,
`maim | imagemagick | gnome-screenshot` for screenshot capture, and
`xclip | xsel` for clipboard access. A GUI package manager must resolve these,
not just GTK/WebKit. The first screenshot choice is `maim`; the backend also
recognizes ImageMagick's `import` and `gnome-screenshot`. These tools operate in
the logged-in user's X11 session with its display authorization. Native Wayland
input is not supported by `xdotool`; Xwayland does not grant control over the
whole Wayland desktop. The backend's optional `grim` capture attempt does not
change that limitation. Full attended capture/input acceptance targets **X11**;
the installer never changes the session type or installs a privileged input service.

The package also declares `Recommends: fonts-noto-cjk`, because minimal desktop
images can otherwise render Chinese as missing-glyph boxes. Normal GUI/APT
dependency resolution with recommendations enabled selects the distro's CJK
font package. It is not bundled in YuDesk; its licensing and updates remain
managed by the distribution. Administrators who disable recommendations must
provide suitable CJK fonts themselves. The packaging regression test uses default
APT recommendations and checks that this font is selected or already installed.

No package contains `preinst`, `postinst`, `prerm`, `postrm`, launchd agents,
systemd units, autostart entries, login items, account creation or privilege
helpers. The pre-existing files `packaging/linux/yudesk-*.service`, timer and
`packaging/macos/com.yudesk.*.plist` are **not included**. Distribution-owned icon
theme triggers may refresh their own caches. Nothing starts or terminates a
YuDesk process, including during upgrade/removal. Existing sessions can finish;
the updated program is used on the next user launch. Finish a session before
upgrading when consistency between already-running code and new helpers matters.

The payload never writes to a home directory, `/etc`, `/var`, or YuDesk's
per-user state. Identity/settings remain in Go's user config directory:
`~/Library/Application Support/yudesk` on macOS and `$XDG_CONFIG_HOME/yudesk`
(normally `~/.config/yudesk`) on Linux. Custom state directories are untouched.
Debian purge also has no hook to remove those directories. macOS bundle updates
use `BundleOverwriteAction=update`, preserving unlisted bundle contents; users
should still keep mutable state outside the application bundle. Existing legacy
services are neither migrated nor disabled by these desktop packages.

## Linux release targets and prerequisites

Supported packaging targets are explicit; **not all Linux distributions**:

| `--linux-suite` | Required build/container release | GTK runtime name |
| --- | --- | --- |
| `ubuntu22.04` | Ubuntu 22.04 amd64 | `libgtk-3-0` |
| `ubuntu24.04` | Ubuntu 24.04 amd64 | `libgtk-3-0t64` |
| `debian12` | Debian 12 amd64 | `libgtk-3-0` |
| `debian13` | Debian 13 amd64 | `libgtk-3-0t64` |

All use `libwebkit2gtk-4.1-0` (not WebKitGTK 4.0 or GTK 4). Package policy requires
GTK >= 3.24 and WebKitGTK >= 2.40; actual symbol-derived dependencies may raise
those floors. No artificial upper bounds prevent security updates. Current
Ubuntu 22.04 repositories supply WebKitGTK 4.1, including security updates; an
unupdated/offline installation may not satisfy the floor. See the official
[Ubuntu 22.04 package](https://packages.ubuntu.com/jammy/libwebkit2gtk-4.1-0),
[Ubuntu 24.04 package](https://packages.ubuntu.com/noble/libwebkit2gtk-4.1-0), and
[Debian 12 package](https://packages.debian.org/bookworm/libwebkit2gtk-4.1-0).

Use each release's system repository libraries, headers, symbol metadata, and
compiler to build the supplied helper. The packager validates the host release,
ELF amd64 headers and GTK/WebKit `DT_NEEDED` entries, then uses `dpkg-shlibdeps` on
both binaries without executing them or using `ldd`. Unresolved dependencies or
symbol warnings fail the build. RPATH/RUNPATH and bundled non-system libraries
are not supported. A binary built on a newer release can require newer glibc;
renaming its package does not make it compatible with an older release. Use
separate suite-labelled artifacts until a cross-release artifact is actually
tested. No claim is made for Ubuntu 20.04, Debian 11, other distros, Linux arm64,
future releases, headless systems, or all Wayland/X11 capture/input features.

Packaging prerequisites: Bash, `dpkg-dev`, `binutils`, `desktop-file-utils`,
standard coreutils, and the target libraries with their package metadata.
Native helper build prerequisites: `build-essential`, `pkg-config`,
`libgtk-3-dev`, `libwebkit2gtk-4.1-dev`, `libjson-glib-dev`. JSON-GLib is used by
the helper's JSON protocol; its runtime dependency is discovered automatically.
Verification additionally uses Python 3
and ShellCheck. Install these **inside the selected build image**, for example:

```sh
apt-get update
apt-get install --no-install-recommends build-essential pkg-config dpkg-dev binutils \
  desktop-file-utils libgtk-3-dev libwebkit2gtk-4.1-dev libjson-glib-dev python3 shellcheck
```

The following are release-engineer commands, not end-user instructions. Both
directories must already exist; the staging directory contains the two prebuilt
binaries. Substitute the project's real release contact for the maintainer:

```sh
bash scripts/package-unix.sh --target linux --arch amd64 \
  --linux-suite ubuntu22.04 --maintainer 'Release team <actual-release-email>' \
  --build-commit "$(git rev-parse HEAD)" \
  --staging-dir /work/staging/ubuntu22.04-amd64 --output-dir /work/packages
```

Output: `YuDesk-2.0.0-1-linux-amd64-ubuntu22.04.deb` and its SHA-256 on stdout.
The package manager resolves the declared runtime libraries. The helper is not
statically bundled with GTK/WebKit. The existing SVG supplies the blue Yu icon
at `/usr/share/icons/hicolor/scalable/apps/yudesk.svg`.

## macOS build host and flags

Run on an actual Mac with Command Line Tools and `xcrun clang`, `lipo`, `otool`,
`plutil`, `pkgbuild`, `productbuild`, `iconutil`, `stat`, `shasum` and `codesign`. The existing macOS
15.7.4 arm64 host may provide these; presence of `/Library/Developer/CommandLineTools`
alone is not proof all prerequisites work. The packager reports missing tools
and does not SSH to or configure that host.

An arm64 packaging host can package either **thin** x86_64 or arm64 supplied
binaries without running them. Use a separate staging directory and invocation
for each architecture, supplying all three regular files: `yudesk`,
`yudesk-window`, and `yudesk-launcher`. The last is built separately from
`native/macos/Launcher.swift`; the packager never builds it or uses the core
as a substitute. `yudesk-window` must link AppKit and WebKit, and
`yudesk-launcher` must link AppKit. Linked
libraries must come from `/System/Library` or `/usr/lib`. The Swift helper may
reference system `@rpath/libswift*.dylib` when its first runpath is `/usr/lib/swift`
and the declared minimum is at least macOS 12.3; these runtime libraries can
reside in Apple's shared cache. Other unresolved `@rpath` dependencies are rejected.
`--macos-min-version` is explicit and must be at least
the deployment target embedded in **all three** binaries. Build helpers and Go
binaries for the desired minimum rather than silently claiming older support.

```sh
bash scripts/package-unix.sh --target macos --arch arm64 \
  --build-commit "$(git rev-parse HEAD)" \
  --macos-min-version 13.0 --staging-dir /work/staging/macos-arm64 \
  --output-dir /work/packages
bash scripts/package-unix.sh --target macos --arch amd64 \
  --build-commit "$(git rev-parse HEAD)" \
  --macos-min-version 13.0 --staging-dir /work/staging/macos-amd64 \
  --output-dir /work/packages
```

The Go inputs inspected on 2026-09-09 declare Mach-O minimum macOS 13.0
on both architectures; their supplied Swift helpers declare 12.3. Therefore
these release inputs require `13.0` or newer in the package and download listing.
The Mac marker relocation required rebuilt Go cores that read Resources; the
release owner rebuilt all desktop Go cores for source/binary consistency. Linux
revision 3 now contains the confirmed `691d0d70...` Go core, keeps its sibling
marker and unchanged `95f05a1a...` native helper. Mac revision 4 is the latest
verified package revision. Do not reuse old archive input hashes; no old Mac
input archive or failed revision-3 attempt is a valid final package. See the
verification record below for the selected output hashes and acceptance limits.
This is the binary deployment requirement, not a claim of GUI acceptance on 13.0.
Outputs: `YuDesk-2.0.0-1-macos-arm64.pkg` and
`YuDesk-2.0.0-1-macos-amd64.pkg`. Installer checks architecture and minimum OS.
Payload is `/Applications/YuDesk.app`, with bundle ID `com.yudesk.desktop` and
product version `2.0.0`. `pkgbuild` creates a component package; `productbuild`
wraps it with the graphical installation flow. Bundle location searching is
disabled so a downloaded/moved copy is not unexpectedly targeted for an update.

The native AppKit asset renderer reproduces the existing SVG's blue gradient,
rounded square, and Yu paths into ten standard iconset PNGs. The packaging script
then runs Apple's **actual `iconutil`** to create `Contents/Resources/YuDesk.icns`.
The asset renderer is compiled for the build host, is not the window helper, and
is not shipped. No generated `.icns` from a non-Mac substitute is committed.

Optional signing flags are `--app-sign-identity 'Developer ID Application: …'`
and `--installer-sign-identity 'Developer ID Installer: …'`, using identities
already available in the operator's keychain. Installer signing requires app
signing; the script signs the Go core, window helper and launcher, then the app
with hardened runtime and timestamps. It separately verifies all three code
signatures and the complete app, checks each executable is still `0755` and the
marker is `0644`, and signs the outer package. `pkgbuild` applies system ownership
to the payload; inspect its archive ownership on the target Mac as well.
No signing credentials or keychain passwords are embedded or requested.

**Without supplied Developer ID identities, output is an unsigned installer
containing an ad-hoc-signed app.** After all resources, notices, marker and plist
are assembled, the default branch signs each copied executable with
`codesign --force --sign - --timestamp=none`, then signs the whole `.app` the
same way. A bare launcher signature is not a bundle resource seal. Both signing
branches strictly verify the three executables and run
`codesign --verify --strict --deep --verbose=2 "$app"` before `pkgbuild`;
any failure aborts packaging. No resource exclusions or validation bypasses are
used. This local integrity signature is not Developer ID approval or notarization.
See Apple's [code-signing guidance](https://developer.apple.com/library/archive/technotes/tn2206/_index.html)
on resource envelopes, nested code and signing from the inside out.
Even when signing flags are supplied, the packager does **not** notarize or staple
the package. Public, friction-free GUI distribution still requires the release
owner to sign, submit to Apple, staple, and verify on a clean Mac. Do not instruct
users to remove quarantine, disable Gatekeeper/SIP, or bypass security prompts;
the packager does none of those things. TCC permissions such as screen recording
and accessibility remain explicit operating-system choices. The bundle allows
local networking for its local dashboard, not arbitrary remote insecure loads.

**macOS input prerequisite:** the current `internal/desktop/platform_unix.go`
backend invokes Apple's `screencapture`, `pbcopy`, and `pbpaste`, but invokes the
third-party `cliclick` executable for mouse/keyboard injection. This package
does **not** include cliclick: no compatible, licensed, architecture-verified
prebuilt copy was supplied for distribution. Receiving remote input therefore
requires an administrator to provision a compatible version and explicitly
grant the relevant Accessibility permission; capture separately needs Screen
Recording permission. `cliclick` must be discoverable through the environment
of YuDesk **launched from Finder**, not merely an interactive shell. The current
Go runner uses ordinary `exec.CommandContext` PATH lookup; installing a copy in
a Homebrew directory not on the GUI PATH is insufficient. No PATH mutation,
external tool download, Homebrew install, or helper-service install is performed
by this installer. Both installer pages and packaging output disclose this
limitation. Do not advertise the current macOS package as providing every
remote-control feature on a clean machine. The main integration owns any future
native input implementation or separately verified bundled cliclick support.

## Revisions, licenses, staging and inspection

`--version` defaults to `2.0.0` and must match the repository's release constant;
it does not rebuild or prove the embedded version of an arbitrary supplied
binary. The release owner must supply the matching product build.
Every package requires `--build-commit HEX` and writes a sibling
`.provenance.json` containing that commit, the checked core/helper hashes and
the completed package hash. The consolidated publisher rejects a missing or
mismatched provenance file, preventing an older `.deb` or `.pkg` from being
published beside a newer Windows build. For final packages, pass
`--main-sha256 HEX --helper-sha256 HEX` and, on macOS,
`--launcher-sha256 HEX`, with hashes
confirmed by the corresponding build owners. The packager checks these before
copying, checks the copied bytes again, and refuses publication if any source
changes during packaging. It always prints input hashes and retains
`input-sha256.txt` in the private work directory when `--keep-work` is used.
On macOS, all three input hashes are recorded, and copied bytes are checked
before either ad-hoc or Developer ID signing modifies the copied signatures.
`packaged-sha256.txt` in the retained work directory records post-signing hashes;
these may legitimately differ from `input-sha256.txt`, particularly for the
launcher now sealed as the app's main executable. The original staging SHA guards
remain unchanged and are rechecked before package publication. A missing/empty/symlinked launcher,
wrong launcher architecture, deployment target above the declared minimum,
missing AppKit link, hash mismatch or signing/mode verification failure aborts
packaging. `--launcher-sha256` is rejected for Linux.
`--package-revision N` defaults to `1`, affects archive names and installer
versions, and never changes product `2.0.0`. Debian version is `2.0.0-N`; the
macOS receipt version is `2.0.0.N`, while both bundle product version fields stay
`2.0.0` and `YuDeskPackageRevision` records `N`.

Consolidated `third_party/THIRD_PARTY_NOTICES.txt` is copied byte-for-byte to
`Contents/Resources/licenses` or `/usr/share/doc/yudesk`, alongside any repository
root LICENSE/COPYING/NOTICE files. `--licenses-dir EXISTING_DIR` retains additional
documents and subdirectories under `additional/`; symlinks and special files
are rejected. No license terms are silently replaced. Linux system libraries
retain their distro-provided licensing documents.

Input/output paths are explicit and can contain spaces. The builder writes only
a private `mktemp` child of the output directory and publishes a completed
package with an atomic no-overwrite hard link. Use an output filesystem supporting
hard links (native Linux/macOS storage is recommended; a Windows-mounted output
may not support them). Existing output, symlinked/empty/missing binaries,
incompatible architecture, absent libraries/tools, and wrong suite are errors.
Input binaries are unchanged. No roots, home directories or user-owned output
trees are recursively deleted. `--keep-work` retains the exact payload and
intermediate package for debugging. Otherwise only that invocation's private
temporary directory is removed, including on failure.

Build paths are relocatable; **installed paths are intentionally fixed** for
trusted installed-mode detection. Non-root archive inspection does not install
or execute anything:

```sh
dpkg-deb --info /work/packages/YuDesk-2.0.0-1-linux-amd64-ubuntu22.04.deb
dpkg-deb --contents /work/packages/YuDesk-2.0.0-1-linux-amd64-ubuntu22.04.deb
dpkg-deb --extract /work/packages/YuDesk-2.0.0-1-linux-amd64-ubuntu22.04.deb /work/inspect-linux
# On the target Mac; choose an extraction path that does not already exist:
pkgutil --expand-full /work/packages/YuDesk-2.0.0-1-macos-arm64.pkg /work/inspect-macos
pkgutil --check-signature /work/packages/YuDesk-2.0.0-1-macos-arm64.pkg
```

## Verification

Run `bash scripts/package-unix.test.sh` as an unprivileged user inside each
matching Linux build image with the test prerequisites above. It compiles two
inert ELF fixtures linked against real distro GTK/WebKit libraries, packages and
extracts them, and verifies modes/ownership, binary and license byte identity,
the marker, launcher, checksums, hook absence, negative inputs, and no-clobber
behavior. It runs APT in simulation mode to verify that the complete dependency
graph, including input/capture/clipboard tools and the recommended CJK font,
resolves on that release with default APT recommendations. It
never runs the fixture binaries or installs a `.deb`. It also parses
the two macOS architecture templates; this is not macOS build verification.

`python3 packaging/macos/tests/test-signing-order.py` also exercises the actual
script's signing block with a recording test double: both identity branches,
inside-out order, strict/deep verification before packaging, and every signing
or verification failure stopping the build. It also executes the real marker
writer and both platform call sites: Mac Resources-only location for Intel and
arm64, Linux sibling location, complete JSON paths/schema and mode `0644`.
This is layout and command-flow regression,
not Apple signature verification. After this fix, rebuild old unsealed Mac
packages in a fresh output directory and use real `codesign` to verify the app
in the retained payload and again after `pkgutil --expand-full`; do not publish
an earlier package merely because its bare input signatures verified.

Target-host acceptance still requires real product binaries: open the native
frameless window from Applications/the menu, query installed mode, confirm the
sibling helper is used, check explicit OS permissions, and preserve device
identity through an upgrade/removal. Run signed/notarized `.pkg` acceptance on
both Intel and Apple silicon and GUI `.deb` acceptance on the declared distro
releases before publication. Production remains unchanged until integration
and release verification are complete.

On 2026-09-09 the packaging suite passed in non-root Ubuntu 22.04, Debian 12 and
Debian 13 containers. The real Ubuntu 22.04-built `.deb` containing the final
supplied Go main and native helper passed archive/byte verification and APT
dependency simulation on Ubuntu 22.04/24.04 and Debian 12/13. No YuDesk package was
installed. This is not native GUI acceptance. The release workspace retains
input hashes, full archive inspection output, dependency simulation evidence,
the revision-3 `.deb`, preserved historical revision-1/2 packages and their staging,
and historical target-Mac source/input handoffs under
`.smoke/installed-release-20260909.1/unix-installers/`.

The revision-1 Linux package pins the superseded helper snapshot `bdfdf1b4...`
and must not be released. The native agent has now frozen the replacement
`95f05a1abeaffdea922fc20175019cc5caa22d2edd022096046a19bfca5620cd`;
revision 2 pins that helper and the unchanged Go core `7e3943d1...`, and adds
`Recommends: fonts-noto-cjk`. Revision 1 remains unchanged as historical evidence.
The native agent's reported sandboxed Xvfb clipboard/download/Go integration
and Mac LaunchServices acceptance are recorded separately in `UNIX_WINDOW.md`;
they do not replace target-system installer or public signing acceptance.

Both the historical revision-2 archive and the final revision-3 archive passed non-root byte/marker/ownership inspection
and default-recommendations APT simulation on all four releases above. All four
simulations explicitly selected `fonts-noto-cjk`: Ubuntu 22.04 / Debian 12
`1:20220127+repack1-1`, Ubuntu 24.04 `1:20230817+repack1-3`, Debian 13
`1:20240730+repack1-1` in the tested repository snapshots. These are observed
solver choices, not pinned font versions or a new graphical rendering test.

### Final selected artifacts — 2026-09-09

| Artifact | SHA-256 |
| --- | --- |
| Linux amd64, Ubuntu 22.04 baseline, revision 4 | `1b3cee47435dc7609faa2243495190e9914c51f9c43f8e647832054ea77030b6` |
| macOS arm64, revision 4 (main task reported) | `3485aefe2782f77350a5816a792bf9c38c5baef3f65b3dfeacf1ed19a26beaae` |
| macOS Intel, revision 4 (main task reported) | `37aa9e468a2c2a2b1bf06303ae11dbe0916a3b81366d0daeb901e28a754f8275` |

The real Linux revision-4 package was built without root from Go main
`691d0d70a45dc43f19b06459f76edbab6b6bce8210bb723a2784a47461376206`
and native helper
`5806d20e12ce437b8ab2db1613352c778f2fb3a4c4e403ae284701ca0b8a59f5`.
Source/staging/extracted binary hashes, root archive ownership, `0755` executables,
`0644` sibling marker with revision 4, icon/notices, md5sums, desktop entry and
absence of services/hooks all passed. `Recommends: fonts-noto-cjk` remains present.
Default APT simulations passed on Ubuntu 22.04/24.04 and Debian 12/13 and selected
the font. Bash syntax, ShellCheck and all four marker/signing-flow regression
tests passed. Full evidence is in
`.smoke/installed-release-20260909.1/unix-installers/REVISION3_VERIFICATION.md`.
The dependency graph is unchanged from revision 3. Revision 4 contains the
final native-window fullscreen fallback for a reproduced WebKitGTK 2.50.4 DOM
fullscreen abort. Three fullscreen/restore/hide/show cycles and Escape exit
passed on an isolated display; synthetic page clicks cannot enter fullscreen.
Its extracted Go/helper/notices bytes and the archive ownership/marker/no-hooks
checks were repeated successfully. No actual Linux installation was performed.

The main task reports that **both Mac revision-4 packages were built on a real
Mac and expanded for inspection**. The Resources marker and ad-hoc bundle seal
passed actual strict/deep codesign verification after expansion. Root ownership
and `0755`/`0644` modes in the BOM, the three executables, minimum macOS 13,
ICNS icon and licensing-document consistency all passed. These are main-task
results, not locally repeated Apple verification by this packaging task.
**No actual Mac installation, Gatekeeper acceptance or notarization was tested.**
The app's signature is ad-hoc, not Developer ID; the outer installer remains
unsigned. This does not establish Intel/macOS 13 GUI behavior, OS permissions,
or complete remote-input readiness (cliclick is still not bundled).

All previous Mac input archives are **superseded candidates, not release inputs**:
`YuDesk-2.0.0-macos-packaging-inputs.tar.gz`,
`YuDesk-2.0.0-macos-packaging-inputs-launcher.tar.gz`, and even the historically
named `YuDesk-2.0.0-macos-packaging-inputs-final.tar.gz`. Their embedded scripts,
Go/helpers and old handoff hashes predate subsequent fixes. Keep them only as
evidence; do not publish them or rebuild a release from them. Linux revisions
1/2/3 likewise remain historical; revision 4 supersedes them. No production
deployment or client start/stop was performed by this packaging task.
