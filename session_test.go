package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionFilePermissionsAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "session.json")
	if err := saveSession(path, testSession()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("session mode = %o, want 600", info.Mode().Perm())
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSession(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("insecure session should be rejected: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"secret":"fixture-secret",`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSession(path); err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("corrupt session must fail without leaking data: %v", err)
	}
}
