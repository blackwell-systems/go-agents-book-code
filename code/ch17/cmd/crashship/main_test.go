package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIngestSecret(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	open := filepath.Join(dir, "open")
	if err := os.WriteFile(good, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(open, []byte("s3cret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		env  map[string]string
		want string // "" means an error
	}{
		{map[string]string{"TRIAGE_INGEST_SECRET_FILE": good}, "s3cret"},
		{map[string]string{"TRIAGE_INGEST_SECRET_FILE": open}, ""},
		{map[string]string{"TRIAGE_INGEST_SECRET_FILE": good,
			"TRIAGE_INGEST_SECRET": "other"}, "s3cret"},
		{map[string]string{"TRIAGE_INGEST_SECRET": "env"}, "env"},
		{map[string]string{}, ""},
	}
	for _, tt := range tests {
		got, err := ingestSecret(func(k string) string { return tt.env[k] })
		if (err != nil) != (tt.want == "") || got != tt.want {
			t.Errorf("%v: %q, %v; want %q", tt.env, got, err, tt.want)
		}
	}
}

func TestCheckURL(t *testing.T) {
	for url, ok := range map[string]bool{
		"http://localhost:8417/crashes":      true,
		"http://127.0.0.1:8417/crashes":      true,
		"https://triage.example.com/crashes": true,
		"http://triage.example.com/crashes":  false,
		"ftp://localhost/crashes":            false,
	} {
		if err := checkURL(url); (err == nil) != ok {
			t.Errorf("checkURL(%q): %v", url, err)
		}
	}
}
