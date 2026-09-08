"""Inspect Debian archive metadata without installing or executing its contents."""
import io
import json
import pathlib
import plistlib
import subprocess
import sys
import tarfile
import xml.etree.ElementTree as ET

package, repository = map(pathlib.Path, sys.argv[1:])
version = subprocess.check_output(["dpkg-deb", "--field", str(package), "Version"], text=True).strip()
product_version, revision = version.rsplit("-", 1)
assert product_version == "2.0.0" and revision.isdecimal() and int(revision) > 0, version
payload = subprocess.check_output(["dpkg-deb", "--fsys-tarfile", str(package)])
with tarfile.open(fileobj=io.BytesIO(payload), mode="r:") as archive:
    files = {entry.name.removeprefix("./"): entry for entry in archive}
    for name, entry in files.items():
        assert entry.uid == entry.gid == 0, (name, "non-root owner")
        if not entry.issym():
            assert not entry.mode & 0o022, (name, "writable by group/others")
        assert not entry.mode & 0o6000, (name, "setuid/setgid")
        assert name in ("", ".", "usr") or name.startswith("usr/"), name
    for name in ("yudesk", "yudesk-window"):
        entry = files[f"usr/lib/yudesk/{name}"]
        assert entry.isfile() and entry.mode == 0o755
    assert "usr/lib/yudesk/yudesk-launcher" not in files
    marker_entry = files["usr/lib/yudesk/yudesk-install.json"]
    assert marker_entry.mode == 0o644
    marker = json.load(archive.extractfile(marker_entry))
    assert marker == {
        "schemaVersion": 1, "product": "YuDesk", "version": "2.0.0",
        "packageRevision": revision, "platform": "linux", "architecture": "amd64",
        "installed": True, "executable": "/usr/lib/yudesk/yudesk",
        "helper": "/usr/lib/yudesk/yudesk-window",
    }
    assert files["usr/bin/yudesk"].issym()
    assert files["usr/bin/yudesk"].linkname == "../lib/yudesk/yudesk"
    assert not any("systemd" in name or "autostart" in name for name in files)
control = subprocess.check_output(["dpkg-deb", "--ctrl-tarfile", str(package)])
with tarfile.open(fileobj=io.BytesIO(control), mode="r:") as archive:
    names = {entry.name.removeprefix("./") for entry in archive if entry.isfile()}
    assert names == {"control", "md5sums"}, names  # No maintainer scripts or service hooks.
depends = subprocess.check_output(["dpkg-deb", "--field", str(package), "Depends"], text=True)
assert "libc6 (>= " in depends and "libwebkit2gtk-4.1-0" in depends and "libgtk-3-0" in depends
assert "xdotool" in depends
assert "maim | imagemagick | gnome-screenshot" in depends
assert "xclip | xsel" in depends

# Real macOS binaries/pkgbuild are required for an actual .pkg test. These checks
# only verify the templates/schema; they deliberately do not simulate success.
macos = repository / "packaging/macos"
for arch in ("x86_64", "arm64"):
    def render(name):
        result = (macos / name).read_text()
        for key, value in {"VERSION": "2.0.0", "REVISION": "1", "MACOS_MIN": "13.0", "MACH_ARCH": arch}.items():
            result = result.replace(f"@{key}@", value)
        return result.encode()
    info = plistlib.loads(render("Info.plist.in"))
    assert info["CFBundleExecutable"] == "yudesk-launcher"
    assert info["LSUIElement"] is True
    assert info["CFBundleVersion"] == info["CFBundleShortVersionString"] == "2.0.0"
    assert info["CFBundleIconFile"] == "YuDesk.icns"
    distribution = ET.fromstring(render("Distribution.xml.in"))
    assert distribution.find("options").get("hostArchitectures") == arch
    assert distribution.find("options").get("require-scripts") == "false"
    assert distribution.find("volume-check/allowed-os-versions/os-version").get("min") == "13.0"
component = plistlib.loads((macos / "components.plist").read_bytes())[0]
assert component["RootRelativeBundlePath"] == "Applications/YuDesk.app"
assert component["BundleIsRelocatable"] is False
assert component["BundleOverwriteAction"] == "update"
print("Archive ownership, contents, hook absence, dependency and macOS template checks passed")
