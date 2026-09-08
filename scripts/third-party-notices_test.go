package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixtureFile(t *testing.T, root, name, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeModuleStream(t *testing.T) {
	modules, err := decodeModules(strings.NewReader(`{"Path":"example.org/main","Main":true,"Dir":"/main"}
{"Path":"example.org/dep","Version":"v1.0.0","Replace":{"Path":"./dep","Dir":"/main/dep"}}`))
	if err != nil || len(modules) != 2 || modules[1].Replace.Dir != "/main/dep" {
		t.Fatalf("modules=%+v err=%v", modules, err)
	}
	if _, err := decodeModules(strings.NewReader(`{"Path":`)); err == nil {
		t.Fatal("accepted truncated go list output")
	}
}

func TestCollectEffectiveSourcesAndDeterminism(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	local := filepath.Join(root, "third_party", "anet")
	cacheDir := filepath.Join(cache, "example.org", "!capital@v2.0.0")
	fixtureFile(t, local, "LICENSE", "Local MIT license\r\nCopyright local\r\n")
	fixtureFile(t, local, "LICENSE-GO", "Go BSD license\n")
	fixtureFile(t, local, "LICENSES/MIT.txt", "Permission is hereby granted\n")
	fixtureFile(t, local, "nested/NOTICE.md", "Nested notice\n")
	fixtureFile(t, local, "nested/LICENSE_list", "Nested BSD license\n")
	fixtureFile(t, local, "LICENSES/README.md", "License directory documentation\n")
	fixtureFile(t, local, "license.go", "package source\n")
	fixtureFile(t, local, "notice.png", "image\x00")
	fixtureFile(t, local, ".git/LICENSE", "metadata\n")
	fixtureFile(t, cacheDir, "COPYING.txt", "Replacement license\n")
	upstream := t.TempDir()
	fixtureFile(t, upstream, "LICENSE", "WRONG upstream license\n")
	modules := []goModule{
		{Path: "example.org/z", Version: "v1.0.0", Dir: upstream, Replace: &goModule{Path: "./third_party/anet", Dir: local}},
		{Path: "example.org/main", Main: true, Dir: root},
		{Path: "example.org/a", Version: "v1.2.3", Replace: &goModule{Path: "example.org/Capital", Version: "v2.0.0"}},
	}
	manifest, files, err := collectNotices(modules, cache)
	if err != nil || manifest.Incomplete {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
	if len(manifest.Modules) != 2 || manifest.Modules[0].Path != "example.org/a" {
		t.Fatalf("incorrect module selection/order: %+v", manifest.Modules)
	}
	localEntry := manifest.Modules[1]
	if localEntry.Replace.Path != "./third_party/anet" || len(localEntry.Files) != 5 {
		t.Fatalf("incorrect local replacement: %+v", localEntry)
	}
	for _, file := range localEntry.Files {
		data := files[file.Output]
		if strings.Contains(string(data), "WRONG") || strings.Contains(string(data), "metadata") {
			t.Fatalf("copied unrelated content: %s", file.Output)
		}
		if file.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
			t.Fatalf("missing content hash: %+v", file)
		}
	}
	if got := string(files["licenses/example.org/z@v1.0.0/LICENSE"]); got != "Local MIT license\r\nCopyright local\r\n" {
		t.Fatalf("original bytes changed: %q", got)
	}
	if bytes.Contains(files["manifest.json"], []byte(root)) || bytes.Contains(files["manifest.json"], []byte(cache)) {
		t.Fatal("machine-specific paths leaked into manifest")
	}
	combined := string(files["THIRD_PARTY_NOTICES.txt"])
	if !strings.Contains(combined, "Module: example.org/z\nVersion: v1.0.0\nReplacement: ./third_party/anet\n") ||
		!strings.Contains(combined, "--- File: LICENSE ---\n\nLocal MIT license\r\nCopyright local\r\n") ||
		!strings.Contains(combined, "--- File: LICENSE-GO ---\n\nGo BSD license\n") ||
		!strings.Contains(combined, "Replacement version: v2.0.0\n") ||
		strings.Index(combined, "Module: example.org/a\n") > strings.Index(combined, "Module: example.org/z\n") {
		t.Fatalf("incorrect combined notice: %s", combined)
	}
	modules[0], modules[2] = modules[2], modules[0]
	_, again, err := collectNotices(modules, cache)
	if err != nil || !reflect.DeepEqual(files, again) {
		t.Fatalf("output is not deterministic: %v", err)
	}
	output := filepath.Join(t.TempDir(), "bundle")
	if err := writeBundle(output, files); err != nil {
		t.Fatal(err)
	}
	if err := writeBundle(output, again); err != nil {
		t.Fatalf("identical rerun failed: %v", err)
	}
}

func TestMissingAndNonTextLicensesAreExplicit(t *testing.T) {
	root := t.TempDir()
	fixtureFile(t, root, "notice-only/NOTICE", "Copyright attribution only\n")
	fixtureFile(t, root, "bad/LICENSE", "\x00binary")
	fixtureFile(t, root, "bad/COPYING", " \n\t")
	fixtureFile(t, root, "empty/LICENSE/not-a-license.go", "package license\n")
	manifest, _, err := collectNotices([]goModule{
		{Path: "example.org/main", Main: true, Dir: root},
		{Path: "example.org/missing", Version: "v1.0.0"},
		{Path: "example.org/notice", Version: "v1.0.0", Dir: filepath.Join(root, "notice-only")},
		{Path: "example.org/bad", Version: "v1.0.0", Dir: filepath.Join(root, "bad")},
		{Path: "example.org/empty", Version: "v1.0.0", Dir: filepath.Join(root, "empty")},
	}, t.TempDir())
	if err != nil || !manifest.Incomplete || len(manifest.Modules) != 4 {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
	for _, module := range manifest.Modules {
		if len(module.Warnings) == 0 {
			t.Fatalf("silent omission: %+v", module)
		}
	}
}

func TestCacheFallbackAndLocalReplacementWithoutDir(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	fixtureFile(t, cache, "example.org/!upper@v1.0.0/LICENSE", "Cached license\n")
	fixtureFile(t, root, "fork/COPYING", "Fork license\n")
	manifest, _, err := collectNotices([]goModule{
		{Path: "example.org/main", Main: true, Dir: root},
		{Path: "example.org/Upper", Version: "v1.0.0"},
		{Path: "example.org/local", Version: "v1.0.0", Replace: &goModule{Path: "./fork"}},
	}, cache)
	if err != nil || manifest.Incomplete {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
}

func TestBundleRefusesUnrelatedOrChangedFiles(t *testing.T) {
	for _, existing := range []string{"unrelated.txt", "manifest.json", "licenses/stale@v1/LICENSE"} {
		t.Run(existing, func(t *testing.T) {
			out := t.TempDir()
			fixtureFile(t, out, existing, "keep me\n")
			err := writeBundle(out, map[string][]byte{"manifest.json": []byte("new\n")})
			if err == nil {
				t.Fatal("accepted a nonmatching output directory")
			}
			data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(existing)))
			if err != nil || string(data) != "keep me\n" {
				t.Fatalf("modified existing file: %q %v", data, err)
			}
		})
	}
}

func TestRejectUnsafeModuleAndOutputPaths(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "a/../../outside", `a\b`, "C:/outside"} {
		_, _, err := collectNotices([]goModule{
			{Path: "example.org/main", Main: true, Dir: t.TempDir()},
			{Path: name, Version: "v1.0.0"},
		}, "")
		if err == nil {
			t.Fatalf("accepted unsafe module %q", name)
		}
		if err := writeBundle(t.TempDir(), map[string][]byte{name: []byte("bad")}); err == nil {
			t.Fatalf("accepted unsafe output %q", name)
		}
	}
}

func TestSymlinkLicenseAndOutput(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	fixtureFile(t, target, "LICENSE", "external license\n")
	if err := os.Symlink(filepath.Join(target, "LICENSE"), filepath.Join(root, "LICENSE")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	entry := moduleNotice{Path: "example.org/dep", Version: "v1.0.0"}
	files := make(map[string][]byte)
	collectModule(root, &entry, files)
	if len(files) != 0 || len(entry.Warnings) == 0 {
		t.Fatalf("followed license symlink: %+v", entry)
	}
	out := filepath.Join(t.TempDir(), "out")
	if err := os.Symlink(target, out); err != nil {
		t.Fatal(err)
	}
	if err := writeBundle(out, map[string][]byte{"manifest.json": []byte("{}")}); err == nil {
		t.Fatal("followed output directory symlink")
	}
}

func TestRunOfflineWithRealGoList(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go executable unavailable")
	}
	root := t.TempDir()
	gomod := "module example.org/main\n\ngo 1.25.0\n\nrequire example.org/dep v1.0.0\nreplace example.org/dep => ./fork\n"
	fixtureFile(t, root, "go.mod", gomod)
	fixtureFile(t, root, "fork/go.mod", "module example.org/dep\n\ngo 1.25.0\n")
	out := filepath.Join(t.TempDir(), "bundle")
	var stdout, stderr bytes.Buffer
	args := []string{"-module-dir", root, "-out", out}
	if err := run(args, &stdout, &stderr); err == nil || !strings.Contains(stderr.String(), "example.org/dep@v1.0.0") {
		t.Fatalf("missing license did not fail explicitly: %v %s", err, stderr.String())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("strict failure wrote output: %v", err)
	}
	if err := run(append(args, "-allow-missing"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	var manifest noticeManifest
	if err != nil || json.Unmarshal(data, &manifest) != nil || !manifest.Incomplete {
		t.Fatalf("missing incomplete marker: %s %v", data, err)
	}
	combined, err := os.ReadFile(filepath.Join(out, "THIRD_PARTY_NOTICES.txt"))
	if err != nil || !bytes.Contains(combined, []byte("STATUS: INCOMPLETE")) || !bytes.Contains(combined, []byte("WARNING: no readable LICENSE")) {
		t.Fatalf("combined document silently omitted missing license: %s %v", combined, err)
	}
	fixtureFile(t, root, "fork/LICENSE", "MIT License\nPermission is hereby granted.\n")
	completeOut := filepath.Join(t.TempDir(), "complete")
	if err := run([]string{"-module-dir", root, "-out", completeOut}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || string(unchanged) != gomod {
		t.Fatal("go.mod was changed")
	}
	if _, err := os.Stat(filepath.Join(root, "go.sum")); !os.IsNotExist(err) {
		t.Fatal("go.sum was created")
	}
}

func TestMultipleGraphsKeepVersionsAndExcludeOwnModules(t *testing.T) {
	root := t.TempDir()
	fixtureFile(t, root, "v1/LICENSE", "License for v1\n")
	fixtureFile(t, root, "v2/LICENSE", "License for v2\n")
	fixtureFile(t, root, "fork/LICENSE", "Local license\n")
	modules := []goModule{
		{Path: "example.org/app", Main: true, Dir: root},
		{Path: "example.org/app/mobile", Main: true, Dir: filepath.Join(root, "mobile")},
		{Path: "example.org/app", Version: "v0.0.0", Replace: &goModule{Path: "..", Dir: root}},
		{Path: "example.org/lib", Version: "v1.0.0", Dir: filepath.Join(root, "v1"), SelectedBy: []string{"example.org/app"}},
		{Path: "example.org/lib", Version: "v2.0.0", Dir: filepath.Join(root, "v2"), SelectedBy: []string{"example.org/app/mobile"}},
		{Path: "example.org/fork", Version: "v1.0.0", Replace: &goModule{Path: "./fork", Dir: filepath.Join(root, "fork")}, SelectedBy: []string{"example.org/app"}},
		{Path: "example.org/fork", Version: "v1.0.0", Replace: &goModule{Path: "../fork", Dir: filepath.Join(root, "fork")}, SelectedBy: []string{"example.org/app/mobile"}},
	}
	manifest, files, err := collectNotices(modules, "")
	if err != nil || manifest.Incomplete || len(manifest.Modules) != 3 || len(manifest.MainModules) != 2 {
		t.Fatalf("incorrect union: %+v %v", manifest, err)
	}
	if len(manifest.Modules[0].SelectedBy) != 2 || manifest.Modules[0].Replace.Path != "./fork" {
		t.Fatalf("shared replacement was not deduplicated: %+v", manifest.Modules[0])
	}
	combined := string(files["THIRD_PARTY_NOTICES.txt"])
	if !strings.Contains(combined, "Module: example.org/lib\nVersion: v1.0.0\n") ||
		!strings.Contains(combined, "Module: example.org/lib\nVersion: v2.0.0\n") ||
		strings.Contains(combined, "Module: example.org/app\n") {
		t.Fatalf("incorrect versions or first-party exclusion: %s", combined)
	}
	// Equal module/version identities cannot silently overwrite different sources.
	modules = append(modules, goModule{Path: "example.org/lib", Version: "v1.0.0", Dir: filepath.Join(root, "v2")})
	if _, _, err := collectNotices(modules, ""); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting license sources were accepted: %v", err)
	}
}

func TestRunWithAdditionalModuleGraph(t *testing.T) {
	root := t.TempDir()
	fixtureFile(t, root, "go.mod", "module example.org/app\n\ngo 1.25.0\n")
	fixtureFile(t, root, "mobile/go.mod", "module example.org/app/mobile\n\ngo 1.25.0\n\nrequire (\nexample.org/app v0.0.0\nexample.org/bridge v1.0.0\n)\nreplace example.org/app => ..\nreplace example.org/bridge => ../bridge\n")
	fixtureFile(t, root, "bridge/go.mod", "module example.org/bridge\n\ngo 1.25.0\n")
	fixtureFile(t, root, "bridge/LICENSE", "Bridge license\n")
	out := filepath.Join(t.TempDir(), "union")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-module-dir", root, "-include-module-dir", filepath.Join(root, "mobile"), "-out", out}, &stdout, &stderr); err != nil {
		t.Fatalf("union run failed: %v %s", err, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	var manifest noticeManifest
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.Incomplete || len(manifest.Modules) != 1 || manifest.Modules[0].Path != "example.org/bridge" {
		t.Fatalf("incorrect union manifest: %s %v", data, err)
	}
}
