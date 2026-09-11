package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPackageExtractAndVerify(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "stub.exe")
	payload := filepath.Join(dir, "client.exe")
	setup := filepath.Join(dir, "setup.exe")
	extracted := filepath.Join(dir, "work", "yudesk.exe")
	if err := os.WriteFile(stub, []byte("MZ-stub"), 0600); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 1<<20)
	for i := range want {
		want[i] = byte(i*31 + 7)
	}
	if err := os.WriteFile(payload, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := packageSetup(stub, payload, setup); err != nil {
		t.Fatal(err)
	}
	if _, err := extractPayload(setup, extracted); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if !equalBytes(got, want) {
		t.Fatal("extracted program differs from payload")
	}
}

func TestCorruptAndInvalidPackagesAreRejected(t *testing.T) {
	dir := t.TempDir()
	stub, payload, setup := filepath.Join(dir, "stub"), filepath.Join(dir, "payload"), filepath.Join(dir, "setup")
	if err := os.WriteFile(stub, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := packageSetup(stub, payload, setup); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(setup, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteAt([]byte{0xff}, 4); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if _, err = extractPayload(setup, filepath.Join(dir, "bad")); err == nil {
		t.Fatal("accepted a corrupted payload")
	}
	if _, err = extractPayload(stub, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("accepted a package without a footer")
	}
	if err = packageSetup(stub, payload, payload); err == nil {
		t.Fatal("accepted an output path equal to the payload")
	}
}
