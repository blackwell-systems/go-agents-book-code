package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// secretNames are the variables that hold the program's keys.
var secretNames = []string{"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY",
	"AUDIT_KEY"}

// tag::secrets[]

// takeSecrets reads each key, from its variable or from the file that
// NAME_FILE names, and removes both variables from this process's
// environment before anything starts a child, so no program the harness
// starts inherits a key. The getenv it returns still sees the keys, and
// the files they came from, for the code that needs them.
func takeSecrets() (func(string) string, error) {
	kept := map[string]string{}
	for _, k := range secretNames {
		v, file, err := readSecret(k)
		if err != nil {
			return nil, err
		}
		kept[k], kept[k+"_FILE"] = v, file
		for _, name := range []string{k, k + "_FILE"} {
			if err := os.Unsetenv(name); err != nil {
				return nil, fmt.Errorf("unset %s: %w", name, err)
			}
		}
	}
	return func(k string) string {
		if v, ok := kept[k]; ok {
			return v
		}
		return os.Getenv(k)
	}, nil
}

// end::secrets[]

// tag::read-secret[]

// readSecret returns the key in $k, or else the one in the file that
// $k_FILE names, and the file. A key file anyone but you can read is
// refused: it would be no better than the environment.
func readSecret(k string) (key, file string, err error) {
	key, file = os.Getenv(k), os.Getenv(k+"_FILE")
	if key != "" || file == "" {
		return key, "", nil
	}
	fi, err := os.Stat(file)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", k+"_FILE", err)
	}
	if fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		return "", "", fmt.Errorf("%s: %s is mode %v; make it %v "+
			"(chmod 600)", k+"_FILE", file, fi.Mode().Perm(),
			os.FileMode(0o600))
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", k+"_FILE", err)
	}
	return strings.TrimSpace(string(b)), file, nil
}

// end::read-secret[]

// errKeyReadable is returned when test code could read a key file.
var errKeyReadable = errors.New("test code could read the key file")
