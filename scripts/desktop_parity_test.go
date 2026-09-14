package main

import (
	"os"
	"strings"
	"testing"
)

func TestDesktopReleaseCarriesOneSourceRevision(t *testing.T) {
	build, err := os.ReadFile("build.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"BuildCommit=", "desktop-core-builds.json", "buildvcs=false", "windows", "linux", "darwin"} {
		if !strings.Contains(string(build), expected) {
			t.Errorf("desktop build is missing provenance guard %q", expected)
		}
	}
	if strings.Contains(string(build), `Get-ChildItem -LiteralPath $distRoot -Recurse`) {
		t.Fatal("desktop checksums recursively include historical staging artifacts")
	}
	for _, expected := range []string{"publishedArtifact", "missing release artifact"} {
		if !strings.Contains(string(build), expected) {
			t.Errorf("desktop checksum generation is missing fixed artifact guard %q", expected)
		}
	}
	publish, err := os.ReadFile("package-release.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Mixed desktop revisions are not publishable", "Package provenance mismatch", "packageSha256", "windows/amd64", "linux/amd64", "darwin/amd64", "darwin/arm64"} {
		if !strings.Contains(string(publish), expected) {
			t.Errorf("desktop publication is missing parity check %q", expected)
		}
	}
}
