// third-party-notices collects the current main module's locally available
// dependency licenses. Run from the repository root; see third-party-notices.README.md.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type goModule struct {
	Path, Version, Dir string
	Main               bool
	Replace            *goModule
	SelectedBy         []string
}

type noticeFile struct {
	Path   string `json:"path"` // Relative to the effective module source root.
	Output string `json:"output"`
	SHA256 string `json:"sha256"`
}

type replacement struct {
	Path    string `json:"path"` // Local paths are relative to the main module.
	Version string `json:"version,omitempty"`
}

type moduleNotice struct {
	Path       string       `json:"path"`
	Version    string       `json:"version"`
	Replace    *replacement `json:"replace,omitempty"`
	Files      []noticeFile `json:"files"`
	Warnings   []string     `json:"warnings,omitempty"`
	SelectedBy []string     `json:"selected_by"`
}

type noticeManifest struct {
	SchemaVersion int            `json:"schema_version"`
	MainModule    string         `json:"main_module"`
	MainModules   []string       `json:"main_modules"` // Explicitly excluded first-party modules.
	Incomplete    bool           `json:"incomplete"`
	Modules       []moduleNotice `json:"modules"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("third-party-notices", flag.ContinueOnError)
	flags.SetOutput(stderr)
	moduleDir := flags.String("module-dir", ".", "main module directory (go.work is disabled)")
	includeDir := flags.String("include-module-dir", "", "also collect this main module's graph; both main modules are excluded as first-party")
	out := flags.String("out", "", "output directory; must be empty, new, or contain identical generated files")
	allowMissing := flags.Bool("allow-missing", false, "write an INCOMPLETE bundle with explicit warnings on missing/unreadable licenses")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *out == "" || flags.NArg() != 0 {
		return errors.New("usage: go run scripts/third-party-notices.go -out DIR [-module-dir DIR] [-include-module-dir DIR] [-allow-missing]")
	}
	dirs := []string{*moduleDir}
	if *includeDir != "" {
		dirs = append(dirs, *includeDir)
	}
	var modules []goModule
	for _, dir := range dirs {
		listed, err := offlineGo(dir, "list", "-mod=readonly", "-m", "-json", "all")
		if err != nil {
			return err
		}
		graph, err := decodeModules(bytes.NewReader(listed))
		if err != nil {
			return err
		}
		var owner string
		for _, module := range graph {
			if module.Main {
				owner = module.Path
			}
		}
		if owner == "" {
			return errors.New("go list did not return a main module for an input directory")
		}
		for i := range graph {
			graph[i].SelectedBy = []string{owner}
		}
		modules = append(modules, graph...)
	}
	cache, err := offlineGo(*moduleDir, "env", "GOMODCACHE")
	if err != nil {
		return err
	}
	manifest, files, err := collectNotices(modules, strings.TrimSpace(string(cache)))
	if err != nil {
		return err
	}
	for _, module := range manifest.Modules {
		for _, warning := range module.Warnings {
			fmt.Fprintf(stderr, "WARNING: %s@%s: %s\n", module.Path, module.Version, warning)
		}
	}
	if manifest.Incomplete && !*allowMissing {
		return errors.New("licenses are incomplete (see warnings); no output written; populate missing local sources or explicitly use -allow-missing")
	}
	if err := writeBundle(*out, files); err != nil {
		return err
	}
	status := "complete"
	if manifest.Incomplete {
		status = "INCOMPLETE; see manifest warnings"
	}
	fmt.Fprintf(stdout, "Collected %d modules, %d license/notice files into %s (%s).\n", len(manifest.Modules), len(files)-3, *out, status)
	return nil
}

// Never download modules, edit go.mod/go.sum, inherit a workspace, or print
// subprocess stderr (which can contain authenticated proxy/VCS URLs).
func offlineGo(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		switch strings.ToUpper(key) {
		case "GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "GOWORK", "GOFLAGS":
			continue
		}
		cmd.Env = append(cmd.Env, env)
	}
	cmd.Env = append(cmd.Env, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=")
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s failed offline (%v); check the installed Go version and local module cache; subprocess stderr withheld", strings.Join(args, " "), err)
	}
	return data, nil
}

func decodeModules(r io.Reader) ([]goModule, error) {
	decoder := json.NewDecoder(r)
	var modules []goModule
	for {
		var module goModule
		if err := decoder.Decode(&module); err != nil {
			if errors.Is(err, io.EOF) {
				return modules, nil
			}
			return nil, fmt.Errorf("decode go list module stream: %w", err)
		}
		modules = append(modules, module)
	}
}

func collectNotices(modules []goModule, cache string) (noticeManifest, map[string][]byte, error) {
	manifest := noticeManifest{SchemaVersion: 1, Modules: []moduleNotice{}}
	files := make(map[string][]byte)
	var mainDir string
	owned := make(map[string]bool)
	for _, module := range modules {
		if module.Main {
			if manifest.MainModule == "" {
				manifest.MainModule, mainDir = module.Path, module.Dir
			}
			if !owned[module.Path] {
				manifest.MainModules = append(manifest.MainModules, module.Path)
			}
			owned[module.Path] = true
		}
	}
	sort.Strings(manifest.MainModules)
	if manifest.MainModule == "" || mainDir == "" {
		return manifest, nil, errors.New("go list did not return a main module with a source directory")
	}
	modules = append([]goModule(nil), modules...)
	sort.SliceStable(modules, func(i, j int) bool {
		if modules[i].Path != modules[j].Path {
			return modules[i].Path < modules[j].Path
		}
		return modules[i].Version < modules[j].Version
	})
	seen := make(map[string]int)
	for _, module := range modules {
		if owned[module.Path] {
			continue
		}
		if !safeRelative(module.Path) || strings.ContainsAny(module.Path, "@:") || !safeRelative(module.Version) || strings.ContainsAny(module.Version, "/@:") {
			return manifest, nil, errors.New("invalid module path or version in go list output")
		}
		entry := moduleNotice{Path: module.Path, Version: module.Version, Files: []noticeFile{}}
		effective := module
		if module.Replace != nil {
			effective = *module.Replace
			replacePath := effective.Path
			if effective.Version == "" {
				if effective.Dir == "" {
					effective.Dir = effective.Path
					if !filepath.IsAbs(effective.Dir) {
						effective.Dir = filepath.Join(mainDir, effective.Dir)
					}
				}
				rel, err := filepath.Rel(mainDir, effective.Dir)
				if err != nil {
					return manifest, nil, fmt.Errorf("cannot express local replacement for %s relative to main module", module.Path)
				}
				replacePath = filepath.ToSlash(rel)
				if rel == "." || (!strings.HasPrefix(replacePath, "../") && replacePath != "..") {
					replacePath = "./" + replacePath
				}
			}
			entry.Replace = &replacement{Path: replacePath, Version: effective.Version}
		}
		// Dir can be absent even for an already extracted module whose checksum
		// is absent from go.sum. Read the exact cache version without downloading.
		root := effective.Dir
		if root == "" && effective.Version != "" && cache != "" && safeRelative(effective.Path) && safeRelative(effective.Version) && !strings.ContainsAny(effective.Version, "/:") {
			root = filepath.Join(cache, filepath.FromSlash(escapeModule(effective.Path)+"@"+escapeModule(effective.Version)))
		}
		if root == "" {
			entry.Warnings = append(entry.Warnings, "module source unavailable locally; populate the exact selected module version in GOMODCACHE")
		} else if info, err := os.Stat(root); err != nil || !info.IsDir() {
			entry.Warnings = append(entry.Warnings, "module source directory unavailable locally; populate the exact selected version or restore the local replacement")
		} else {
			collectModule(root, &entry, files)
		}
		if len(entry.Warnings) != 0 {
			manifest.Incomplete = true
		}
		key := module.Path + "@" + module.Version
		if i, ok := seen[key]; ok {
			previous := manifest.Modules[i]
			previous.SelectedBy = nil
			a, _ := json.Marshal(previous)
			b, _ := json.Marshal(entry)
			if !bytes.Equal(a, b) {
				return manifest, nil, fmt.Errorf("conflicting effective sources or license contents for %s across input graphs", key)
			}
			entry.SelectedBy = manifest.Modules[i].SelectedBy
		}
		owners := module.SelectedBy
		if len(owners) == 0 {
			owners = []string{manifest.MainModule}
		}
		for _, owner := range owners {
			found := false
			for _, existing := range entry.SelectedBy {
				found = found || existing == owner
			}
			if !found {
				entry.SelectedBy = append(entry.SelectedBy, owner)
			}
		}
		sort.Strings(entry.SelectedBy)
		if i, ok := seen[key]; ok {
			manifest.Modules[i] = entry
			continue
		}
		seen[key] = len(manifest.Modules)
		manifest.Modules = append(manifest.Modules, entry)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, nil, err
	}
	files["manifest.json"] = append(data, '\n')
	var index strings.Builder
	index.WriteString("Third-party Go module licenses and notices\n\n")
	fmt.Fprintf(&index, "Main modules excluded (first-party): %s\nScope: union of all input go list -m -json all graphs, retaining every selected version.\n", strings.Join(manifest.MainModules, ", "))
	if manifest.Incomplete {
		index.WriteString("STATUS: INCOMPLETE -- missing or unreadable license material; see warnings below.\n")
	}
	index.WriteString("Original file bytes and module-relative paths are retained under licenses/.\nVersions identify the selected modules; replacements identify the effective sources.\n\n")
	for _, module := range manifest.Modules {
		fmt.Fprintf(&index, "%s@%s\n", module.Path, module.Version)
		fmt.Fprintf(&index, "  Selected by: %s\n", strings.Join(module.SelectedBy, ", "))
		if module.Replace != nil {
			fmt.Fprintf(&index, "  replace => %s", module.Replace.Path)
			if module.Replace.Version != "" {
				fmt.Fprintf(&index, "@%s", module.Replace.Version)
			}
			index.WriteByte('\n')
		}
		for _, file := range module.Files {
			fmt.Fprintf(&index, "  %s\n", file.Output)
		}
		for _, warning := range module.Warnings {
			fmt.Fprintf(&index, "  WARNING: %s\n", warning)
		}
		index.WriteByte('\n')
	}
	files["THIRD-PARTY-NOTICES.txt"] = []byte(index.String())
	var combined bytes.Buffer
	combined.WriteString("THIRD-PARTY GO LICENSES AND NOTICES\n\n")
	fmt.Fprintf(&combined, "Main modules excluded (first-party): %s\nScope: union of all input go list -m -json all graphs, retaining every selected version.\n", strings.Join(manifest.MainModules, ", "))
	if manifest.Incomplete {
		combined.WriteString("STATUS: INCOMPLETE -- missing or unreadable license material; see per-module WARNING entries.\n")
	}
	combined.WriteString("License and notice contents below are copied verbatim from the effective module sources.\n")
	for _, module := range manifest.Modules {
		combined.WriteString("\n========================================================================\n")
		fmt.Fprintf(&combined, "Module: %s\nVersion: %s\n", module.Path, module.Version)
		if module.Replace != nil {
			fmt.Fprintf(&combined, "Replacement: %s\n", module.Replace.Path)
			if module.Replace.Version != "" {
				fmt.Fprintf(&combined, "Replacement version: %s\n", module.Replace.Version)
			}
		}
		fmt.Fprintf(&combined, "Selected by: %s\n", strings.Join(module.SelectedBy, ", "))
		for _, warning := range module.Warnings {
			fmt.Fprintf(&combined, "WARNING: %s\n", warning)
		}
		for _, file := range module.Files {
			fmt.Fprintf(&combined, "\n--- File: %s ---\n\n", file.Path)
			combined.Write(files[file.Output])
			// The separator newline belongs to the wrapper, not the source copy.
			combined.WriteByte('\n')
		}
	}
	files["THIRD_PARTY_NOTICES.txt"] = combined.Bytes()
	return manifest, files, nil
}

func collectModule(root string, entry *moduleNotice, files map[string][]byte) {
	hasLicense := false
	err := filepath.WalkDir(root, func(filename string, d fs.DirEntry, walkErr error) error {
		rel, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if walkErr != nil {
			entry.Warnings = append(entry.Warnings, rel+": cannot scan source directory")
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".hg", ".svn", ".bzr":
				return filepath.SkipDir
			}
			return nil // A LICENSE/NOTICE directory is never copied as a file.
		}
		candidate, license := noticeCandidate(rel)
		if !candidate {
			return nil
		}
		if !safeRelative(rel) || !d.Type().IsRegular() {
			entry.Warnings = append(entry.Warnings, rel+": license candidate is not a safe regular file (symlinks are not followed)")
			return nil
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			entry.Warnings = append(entry.Warnings, rel+": cannot read license candidate")
			return nil
		}
		if !plainText(data) {
			entry.Warnings = append(entry.Warnings, rel+": license candidate is empty or not UTF-8 plain text")
			return nil
		}
		output := path.Join("licenses", entry.Path+"@"+entry.Version, rel)
		files[output] = data
		entry.Files = append(entry.Files, noticeFile{Path: rel, Output: output, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
		hasLicense = hasLicense || license
		return nil
	})
	if err != nil {
		entry.Warnings = append(entry.Warnings, "cannot finish scanning module sources")
	}
	if !hasLicense {
		entry.Warnings = append(entry.Warnings, "no readable LICENSE/LICENCE/COPYING/UNLICENSE text found (NOTICE alone is insufficient)")
	}
	sort.Slice(entry.Files, func(i, j int) bool { return entry.Files[i].Path < entry.Files[j].Path })
	sort.Strings(entry.Warnings)
}

func noticeCandidate(rel string) (candidate, license bool) {
	name := strings.ToUpper(path.Base(rel))
	switch strings.ToLower(path.Ext(name)) {
	case "", ".txt", ".md", ".rst", ".text":
	default:
		return false, false // e.g. license.go, notice.png, license_test.js.
	}
	for _, stem := range []string{"LICENSE", "LICENSES", "LICENCE", "LICENCES", "COPYING", "UNLICENSE", "NOTICE", "NOTICES"} {
		if name == stem || strings.HasPrefix(name, stem+".") || strings.HasPrefix(name, stem+"-") || strings.HasPrefix(name, stem+"_") {
			return true, !strings.HasPrefix(stem, "NOTICE")
		}
	}
	if strings.HasPrefix(name, "README") {
		return false, false
	}
	for _, dir := range strings.Split(path.Dir(rel), "/") {
		switch strings.ToUpper(dir) {
		case "LICENSE", "LICENSES", "LICENCE", "LICENCES", "COPYING":
			return true, true // e.g. Pion's LICENSES/MIT.txt (REUSE layout).
		case "NOTICE", "NOTICES":
			return true, false
		}
	}
	return false, false
}

func plainText(data []byte) bool {
	if len(bytes.TrimSpace(data)) == 0 || !utf8.Valid(data) {
		return false
	}
	for _, b := range data {
		if b == 0x7f || (b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f') {
			return false
		}
	}
	return true
}

func safeRelative(p string) bool {
	return p != "" && p != "." && path.Clean(p) == p && !path.IsAbs(p) && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\:\x00")
}

func escapeModule(s string) string {
	var result strings.Builder
	for _, c := range s {
		if c >= 'A' && c <= 'Z' {
			result.WriteByte('!')
			c += 'a' - 'A'
		}
		result.WriteRune(c)
	}
	return result.String()
}

// Preflight every existing entry. Identical reruns are allowed; never remove
// stale files or overwrite a different bundle/unrelated user file.
func writeBundle(out string, files map[string][]byte) error {
	err := filepath.WalkDir(out, func(filename string, d fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) && filename == out {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(out, filename)
		if err != nil {
			return err
		}
		expected, ok := files[filepath.ToSlash(rel)]
		if !ok || !d.Type().IsRegular() {
			return fmt.Errorf("output contains an unrelated file or symlink: %s; use a new/empty directory", filename)
		}
		actual, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, expected) {
			return fmt.Errorf("output file differs: %s; use a new/empty directory", filename)
		}
		return nil
	})
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		if !safeRelative(name) {
			return fmt.Errorf("unsafe output path %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		filename := filepath.Join(out, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			actual, readErr := os.ReadFile(filename)
			if readErr != nil || !bytes.Equal(actual, files[name]) {
				return fmt.Errorf("output changed during generation: %s", filename)
			}
			continue
		}
		if err != nil {
			return err
		}
		_, writeErr := file.Write(files[name])
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
