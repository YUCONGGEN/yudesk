package singleinstance

import (
	"path/filepath"
	"testing"
)

func TestAcquireAllowsOnlyOneOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yudesk.lock")
	first, acquired, err := Acquire(path)
	if err != nil || !acquired {
		t.Fatalf("first acquire: acquired=%v err=%v", acquired, err)
	}
	second, acquired, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if acquired || second != nil {
		t.Fatal("second acquire unexpectedly succeeded")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, acquired, err := Acquire(path)
	if err != nil || !acquired {
		t.Fatalf("acquire after release: acquired=%v err=%v", acquired, err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}
