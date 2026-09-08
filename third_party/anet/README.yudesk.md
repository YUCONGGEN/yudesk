# YuDesk anet compatibility fork

Based on `github.com/wlynxg/anet v0.0.5`. This local replacement fixes Android
`-buildmode=c-shared` linking with Go 1.27.1 while keeping linker checks enabled.
It adds no dependencies and does not change the upstream module path or API.

## Audited changes from v0.0.5

- `interface_android.go`: remove the private `net.zoneCache` and
  `golang.org/x/net/internal/socket.zoneCache` linknames, their replica struct,
  update method/calls, and the now-unused sync/time imports.
- Its IPv6 address-construction branch calls `addressWithNumericZone` in
  `zone.go`. Link-local IPv6 addresses are returned as `*net.IPAddr` with the
  decimal interface index as `Zone`. Ordinary IPv4, global/ULA IPv6 and loopback
  addresses retain their original `*net.IPNet` and prefix. Only scoped IPv6
  entries lose the prefix representation, because `net.IPNet` has no zone field.
- Add regression tests and the c-shared link probe under `testdata/cshared`.
- Bound each netlink receive sequence to one second in total and 1 MiB of
  accumulated response data. `SO_RCVTIMEO` is reset to the remaining budget
  before every receive; `MSG_TRUNC` detects an oversized individual datagram.
  Expiration returns `ETIMEDOUT`, and oversized responses return `EMSGSIZE`.
- Harden netlink message/attribute parsing before indexing or alignment,
  including uint32 length overflow on ARM/386, short `IfAddrmsg`, truncated
  route attributes, and IPv4/IPv6 payload lengths and prefix lengths. Ignore
  non-address attributes when constructing an IP address. Use the bounded
  local parsers consistently and avoid a redundant interface query when
  enumerating all addresses.

Only `interface_android.go` and `netlink_android.go` differ among the upstream
Go files. The other upstream Go files, module file and `LICENSE` are byte-for-byte copies.
`LICENSE-GO` also retains the Go license for the Go-derived netlink implementation;
its existing copyright notice is unchanged. Include these licenses with binary
distributions that use the fork.

Android API detection is unchanged: cgo uses `android_get_device_api_level`.
Android 11+ still enumerates addresses through an **unbound** NETLINK_ROUTE
socket and `RTM_GETADDR`, then obtains interface names, MTUs and flags through
ioctl. It never substitutes `net.Interfaces` for this Android 11+ path. The
upstream limitations remain: only interfaces with assigned IP addresses are
enumerated, and hardware/MAC addresses are unavailable.

## Why the numeric zone matters

Source checks used the exact selected modules and the Go 1.27.1 standard library:

- Pion transport v4.1.0 `stdnet/net.go` calls `anet.Interfaces` and
  `anet.InterfaceAddrsByInterface`, and preserves their address values.
- Pion ICE v4.4.2 `addr.go` accepts `*net.IPAddr` and retains its explicit zone.
  Its `gather.go` marks IPv6 link-local candidates as location-tracked when mDNS
  gathering is disabled (as in YuDesk), but still creates their UDP listeners.
  Therefore merely relying on candidate filtering would leave scoped binds
  dependent on a private name cache.
- Go `net/interface.go` and x/net v0.58.0 `internal/socket/sys_posix.go` both
  fall back to a decimal zone index when interface-name lookup is unavailable.
  Numeric zones use these existing public address/socket paths; this fork does
  not read, write, link or imitate either private cache.

Primary source references:
[anet v0.0.5](https://github.com/wlynxg/anet/tree/v0.0.5),
[Android 11 restrictions](https://developer.android.com/training/articles/user-data-ids#mac-11-plus),
[Pion ICE gather](https://github.com/pion/ice/blob/v4.4.2/gather.go),
[Pion ICE address parsing](https://github.com/pion/ice/blob/v4.4.2/addr.go),
[Pion transport](https://github.com/pion/transport/blob/v4.1.0/stdnet/net.go).

## Module and staging integration

Root `go.mod` replaces anet with `./third_party/anet`; `mobile/go.mod` replaces it
with `../third_party/anet`. Both directives are needed because replacements in a
dependency's go.mod are not inherited by the main module.

`mobile/scripts/verify-local-container.sh` copies `third_party` alongside the
root go.mod in each fresh native stage. The selected gomobile version resolves
a local replacement's `Replace.Dir` into an absolute path in its generated
`gobind` module (`cmd/gomobile/bind.go`, `parseModuleVersions`). No absolute
container path is committed in either source go.mod. The real ARM bind below
confirmed this with x/net v0.58.0 still selected.

## Verification

From this directory on Linux:

```sh
go test -race ./...
go test -race interface_android.go netlink_android.go android_api_level.go \
  zone.go zone_test.go interface_android_test.go netlink_android_test.go
```

The explicit-file test runs the actual Android netlink/ioctl implementation on a
Linux kernel with `SetAndroidVersion(11)`. This exercises the enumeration and
lookup paths without changing production build tags. It does not simulate
Android's SELinux policy. Numeric-zone tests cover scoped/unscoped IPv6, IPv4,
and public UDP address resolution. Netlink tests include a real socket receive
timeout, total deadline enforcement despite continuing replies, the 1 MiB cap,
oversized datagrams, and malformed message/address/attribute boundaries. Errors
propagate through interface enumeration, allowing initial P2P setup to fail
and negotiate relay fallback instead of waiting indefinitely for netlink input.

To reproduce the ARM link probe, use an external output directory:

```sh
CC="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/armv7a-linux-androideabi26-clang" \
GOTOOLCHAIN=local GOOS=android GOARCH=arm GOARM=7 CGO_ENABLED=1 \
go build -buildmode=c-shared \
  -ldflags='-checklinkname=1 -extldflags=-Wl,-z,max-page-size=16384' \
  -o /tmp/libanet-arm.so ./testdata/cshared
```

Completed on 2026-09-08 in `yudesk-android-verify-20260908` with Go 1.27.1,
SDK `/opt/android-sdk`, NDK 27.3.13750724 and API 26:

- Both race test commands passed, including the final bounded-netlink changes.
- c-shared probe linked for armeabi-v7a, arm64-v8a, x86 and x86_64, with
  `-checklinkname=1`. The exported `AnetProbe` keeps Android API detection and
  enumeration reachable by the linker.
- Actual YuDesk `gomobile bind -target=android/arm -androidapi 26` passed from a
  fresh native stage and produced an AAR containing `jni/armeabi-v7a/libgojni.so`.
- The generated module selected x/net v0.58.0 and replaced anet with that native
  stage's absolute `repo/third_party/anet` directory.

Verification artifacts are in `/tmp/yudesk-anet-link.QV8Uy4` in that container;
`yudesk-arm-linkcheck.aar` is a local link-check artifact, not a release.
The final generated gomobile module (including bounded netlink) was retained in
`/tmp/gomobile-work-3542208214`.
No APK was built or published by this compatibility task. Full multi-ABI
gomobile/Java/APK verification and Android 11+ device testing remain separate.
