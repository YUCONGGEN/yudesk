"""Marker layout and command-flow regression; not Apple signature verification."""
import json
import os
import pathlib
import subprocess
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[3]
SCRIPT = (ROOT / "scripts/package-unix.sh").read_text()
START = SCRIPT.index("  # Sign nested code first,")
END = SCRIPT.index("  # Signing changes copied Mach-O bytes,", START)
BLOCK = SCRIPT[START:END]
APP = "/test payload/YuDesk.app"
EXECUTABLES = ("yudesk", "yudesk-window", "yudesk-launcher")


class SigningOrderTest(unittest.TestCase):
    def run_block(self, identity="", fail_at=0):
        # Only this explicit test double runs; no Apple tool or staged code runs.
        prelude = r'''
set -euo pipefail
app='/test payload/YuDesk.app'
executables=(yudesk yudesk-window yudesk-launcher)
app_identity=$TEST_IDENTITY
calls=0
codesign() {
  calls=$((calls + 1))
  printf '%s\037' "$@"
  printf '\n'
  if [ "$calls" = "$TEST_FAIL_AT" ]; then return 42; fi
  return 0
}
'''
        result = subprocess.run(
            ["bash", "-s"], input=prelude + BLOCK + "printf 'READY_FOR_PKG\\n'\n",
            text=True, capture_output=True,
            env={**os.environ, "TEST_IDENTITY": identity, "TEST_FAIL_AT": str(fail_at)},
        )
        calls = [line.split("\x1f")[:-1] for line in result.stdout.splitlines()
                 if "\x1f" in line]
        return result, calls

    def test_sign_and_verify_both_branches(self):
        for identity in ("", "Developer ID Application: Fixture (TEST)"):
            with self.subTest(identity=identity or "ad-hoc"):
                result, calls = self.run_block(identity)
                self.assertEqual(result.returncode, 0, result.stderr)
                sign = (["--force", "--options", "runtime", "--timestamp", "--sign", identity]
                        if identity else ["--force", "--sign", "-", "--timestamp=none"])
                paths = [f"{APP}/Contents/MacOS/{name}" for name in EXECUTABLES]
                expected = [sign + [path] for path in paths + [APP]]
                expected += [["--verify", "--strict", "--verbose=2", path] for path in paths]
                expected += [["--verify", "--strict", "--deep", "--verbose=2", APP]]
                self.assertEqual(calls, expected)
                self.assertIn("READY_FOR_PKG", result.stdout)
                if not identity:
                    self.assertIn("not Developer ID or notarization", result.stderr)

    def test_every_sign_or_verify_failure_stops_before_packaging(self):
        for identity in ("", "Developer ID Application: Fixture (TEST)"):
            for fail_at in range(1, 9):
                with self.subTest(identity=identity or "ad-hoc", fail_at=fail_at):
                    result, calls = self.run_block(identity, fail_at)
                    self.assertEqual(result.returncode, 42)
                    self.assertEqual(len(calls), fail_at)
                    self.assertNotIn("READY_FOR_PKG", result.stdout)

    def test_real_script_boundaries_and_input_hash_guards(self):
        self.assertLess(SCRIPT.index('verify_copies "$app/Contents/MacOS"'), START)
        self.assertLess(SCRIPT.index('iconutil --convert icns'), START)
        self.assertLess(END, SCRIPT.index('  pkgbuild --root'))
        self.assertIn('shasum stat codesign; do need "$command"', SCRIPT)
        self.assertIn('"$work/packaged-sha256.txt"', SCRIPT[END:])
        self.assertIn('verify_inputs_unchanged', SCRIPT[SCRIPT.index('  productbuild "${productbuild_args[@]}"'):])
        self.assertNotIn('--resource-rules', SCRIPT)
        self.assertIn('stat -f \'%Lp\' "$app/Contents/Resources/yudesk-install.json"', SCRIPT[END:])

    def test_real_marker_writer_and_platform_call_sites(self):
        start = SCRIPT.index('write_install_marker() {')
        end = SCRIPT.index('\n}\n', start) + 3
        writer = SCRIPT[start:end]
        mac_call = next(line for line in SCRIPT.splitlines()
                        if line.strip().startswith('write_install_marker ') and ' darwin ' in line)
        linux_call = next(line for line in SCRIPT.splitlines()
                          if line.strip().startswith('write_install_marker ') and ' linux ' in line)
        self.assertLess(SCRIPT.index(mac_call), START)
        for arch in ('amd64', 'arm64'):
            with self.subTest(arch=arch), tempfile.TemporaryDirectory(prefix='yudesk-marker-test-') as directory:
                prelude = '''
set -euo pipefail
version=2.0.0
revision=3
arch=$TEST_ARCH
app="$TEST_ROOT/Applications/YuDesk.app"
root="$TEST_ROOT/linux-root"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" "$root/usr/lib/yudesk"
'''
                result = subprocess.run(
                    ['bash', '-s'], input=prelude + writer + mac_call + '\narch=amd64\n' + linux_call,
                    text=True, capture_output=True,
                    env={**os.environ, 'TEST_ROOT': directory, 'TEST_ARCH': arch},
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                mac = pathlib.Path(directory) / 'Applications/YuDesk.app/Contents'
                self.assertFalse((mac / 'MacOS/yudesk-install.json').exists())
                cases = (
                    (mac / 'Resources/yudesk-install.json', 'darwin', arch,
                     '/Applications/YuDesk.app/Contents/MacOS'),
                    (pathlib.Path(directory) / 'linux-root/usr/lib/yudesk/yudesk-install.json',
                     'linux', 'amd64', '/usr/lib/yudesk'),
                )
                for marker, platform, expected_arch, executable_dir in cases:
                    self.assertEqual(marker.stat().st_mode & 0o7777, 0o644)
                    self.assertEqual(json.loads(marker.read_text()), {
                        'schemaVersion': 1, 'product': 'YuDesk', 'version': '2.0.0',
                        'packageRevision': '3', 'platform': platform, 'architecture': expected_arch,
                        'installed': True, 'executable': executable_dir + '/yudesk',
                        'helper': executable_dir + '/yudesk-window',
                    })


if __name__ == "__main__":
    unittest.main()
