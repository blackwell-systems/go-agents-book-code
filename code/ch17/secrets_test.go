package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestTakeSecrets checks that the keys are gone from the program's own
// environment after takeSecrets, from a file or a variable, that the
// getenv it returns still has them, and that a child started without an
// environment of its own inherits none of them.
func TestTakeSecrets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "audit-key")
	if err := os.WriteFile(file, []byte("audit-not-a-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-not-a-real-key-0000")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("AUDIT_KEY", "")
	t.Setenv("AUDIT_KEY_FILE", file)
	getenv, err := takeSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if got := getenv("OPENROUTER_API_KEY"); got != "sk-or-v1-not-a-real-key-0000" {
		t.Errorf("OPENROUTER_API_KEY = %q", got)
	}
	if got := getenv("AUDIT_KEY"); got != "audit-not-a-key" {
		t.Errorf("AUDIT_KEY = %q, want the file's key", got)
	}
	if got := getenv("AUDIT_KEY_FILE"); got != file {
		t.Errorf("AUDIT_KEY_FILE = %q, want %q", got, file)
	}
	for _, k := range secretNames {
		for _, name := range []string{k, k + "_FILE"} {
			if _, ok := os.LookupEnv(name); ok {
				t.Errorf("%s still in the environment", name)
			}
		}
	}
	if _, err := exec.LookPath("env"); err != nil {
		t.Skip("no env command")
	}
	out, err := exec.CommandContext(t.Context(), "env").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range secretNames {
		if strings.Contains(string(out), k) {
			t.Errorf("a child inherited %s", k)
		}
	}
}

// TestKeyFileMode checks that a key file others can read is refused.
func TestKeyFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix permissions")
	}
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte("k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY_FILE", file)
	if _, err := takeSecrets(); err == nil {
		t.Error("takeSecrets accepted a key file anyone can read")
	}
}
