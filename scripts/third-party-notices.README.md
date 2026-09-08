# Go third-party notices

This standard-library-only tool gathers the **current main module's complete
selected module graph**, including indirect and test/tool dependencies reported by
`go list -mod=readonly -m -json all`. It excludes the main module itself. This is a
module inventory, not a claim that every listed module is linked into each binary.

From the repository root:

```sh
go run scripts/third-party-notices.go -out .smoke/license-check
go run scripts/third-party-notices.go -include-module-dir mobile -out .smoke/license-check
go test scripts/third-party-notices.go scripts/third-party-notices_test.go
```

Use `-module-dir DIR` to select another main module. Relative `-out` paths are
resolved against the caller's working directory, not `-module-dir`. The tool runs
Go with `GOWORK=off`, `GOFLAGS=`, `GOTOOLCHAIN=local`, `GOPROXY=off`, and
`GOSUMDB=off`; it neither downloads modules nor changes go.mod/go.sum. Run with an
installed Go version that supports the selected module graph. Child-process
stderr is withheld because proxy/VCS errors can contain credentials.

For the combined desktop/Android release, use `-include-module-dir mobile`.
Both root and mobile graphs are queried independently. Their main module paths
are explicitly excluded as first-party, including mobile's local replacement of
`github.com/yudesk/yudesk`. The union retains every distinct module/version pair:
for example, different selected versions of x/mod and x/tools get separate entries
even when their license text is identical. Each entry's `selected_by` records the
main module(s) selecting it. Shared module/version entries are merged only if
their effective replacement and all collected text/hashes agree. `main_modules`
lists the first-party exclusions; replacement paths are relative to the primary
`-module-dir`. This includes x/mobile and the rest of mobile's complete graph,
without relying on target-specific guesses about which modules are linked.

## Source and filename handling

- Uses the exact selected `Dir`, giving `Replace.Dir` priority. Local replacements
  use their modified local license files, never the original module's cache copy.
  Both original and replacement versions/paths appear in the inventory; local
  paths are relative to the main module, with no absolute cache paths recorded.
- If `go list` omits `Dir`, checks the exact version's extracted GOMODCACHE path,
  including Go's uppercase escaping. No fallback to a different version, no zip
  extraction, and no network fetch. Missing extracted sources are explicit gaps.
- Recursively includes regular LICENSE, LICENCE, COPYING, UNLICENSE, NOTICE files
  and variants such as LICENSE-GO, LICENSE-MMAP-GO, LICENSE_list and NOTICE.txt.
  Also collects text files inside LICENSE(S)/LICENCE(S)/COPYING/NOTICE(S)
  directories, including Pion's `LICENSES/MIT.txt`. Directories themselves are
  never read as license files. Code/image extensions and license-directory README
  files are excluded; VCS metadata is skipped. Nested license paths are retained.
- Reads nonempty UTF-8 plain text (`.txt`, `.md`, `.rst`, `.text`, or extensionless)
  without changing its bytes or line endings. Empty, binary, unreadable or symlink
  license candidates produce warnings. Directory symlinks are not traversed.
  NOTICE by itself does not satisfy the module's license requirement. This uses
  filename/layout conventions, not automatic legal classification of prose.

## Output and missing licenses

The output contains:

```text
THIRD_PARTY_NOTICES.txt
THIRD-PARTY-NOTICES.txt
manifest.json
licenses/<original-module-path>@<selected-version>/<original-relative-path>
```

`THIRD_PARTY_NOTICES.txt` is the standalone release document for the website,
APK assets, and distribution packages. It concatenates every collected file's
original bytes with module, version, replacement and relative filename headings,
in deterministic module/path order. It also retains all missing-license warnings.
The separate `THIRD-PARTY-NOTICES.txt` is a short index of the directory copies.
The JSON manifest records module/replacement versions,
relative input and output paths, and SHA-256 for every copied file. Paths and
entries are sorted; there are no generation timestamps or machine-specific cache
paths. Identical inputs produce byte-identical output, and rerunning against an
identical bundle succeeds. A directory containing different or unrelated files
is rejected before writing: choose a new/empty directory after dependency or
license changes. The tool never deletes output or overwrites differing files.

Missing source directories, missing LICENSE/COPYING text, and unreadable/non-text
license candidates print per-module warnings. **By default any gap causes a
nonzero exit before output is written.** For an explicit diagnostic bundle:

```sh
go run scripts/third-party-notices.go -out .smoke/license-check -allow-missing
```

That mode exits successfully only after writing, prints the same warnings, sets
`incomplete: true` in manifest.json, and prominently marks both text documents
INCOMPLETE. It does not silently omit missing modules. Resolve the recorded gaps
before treating it as a complete notice bundle.

## Existing verification container

The mounted `/src` repository and already extracted cache can be used offline:

```sh
docker exec -w /src -e GOPROXY=off -e GOSUMDB=off -e GOTOOLCHAIN=local yudesk-android-verify-20260908 go test scripts/third-party-notices.go scripts/third-party-notices_test.go
docker exec -w /src -e GOPROXY=off -e GOSUMDB=off -e GOTOOLCHAIN=local yudesk-android-verify-20260908 go run scripts/third-party-notices.go -out .smoke/license-check -allow-missing
```

Tests cover streamed Go JSON, real offline `go list` with a local replacement,
strict and warning modes, exact cache versions and case escaping, anet-style
LICENSE-GO and Pion-style LICENSES, nested files, byte preservation, deterministic
merged notices and reruns, missing/binary licenses, symlinks, and protecting
existing output files.
Additional tests cover the root/mobile graph union, first-party exclusions,
version retention, graph provenance, and conflicting source rejection.
