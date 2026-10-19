package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io/fs"
	"strings"
)

// maxRead is the most of one file the agent gets back, so a large file
// can't fill the conversation.
const maxRead = 20_000

// tag::tools[]

// fileTools returns the agent's two tools, which read only from
// files. Give it os.Root's FS: a root refuses any path that leads
// outside its directory, through ".." or a symbolic link.
func fileTools(files fs.FS) []Tool {
	return []Tool{{
		Name:        "list_files",
		Description: "List every file in the directory, one path per line.",
		Parameters:  []byte(`{"type": "object", "properties": {}}`),
		Run: func(context.Context, string) (string, error) {
			return listFiles(files)
		},
	}, {
		Name: "read_file",
		Description: "Read one file. path is relative to the directory, " +
			"as list_files prints it.",
		Parameters: []byte(`{"type": "object", "properties": {"path":
			{"type": "string"}}, "required": ["path"]}`),
		Run: func(_ context.Context, args string) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("arguments: %w", err)
			}
			return readFile(files, a.Path)
		},
	}}
}

// end::tools[]

// listFiles walks the directory and returns its files' paths.
func listFiles(files fs.FS) (string, error) {
	var paths []string
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry,
		err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("list files: %w", err)
	}
	return strings.Join(paths, "\n"), nil
}

// readFile returns one file's text, cut to maxRead bytes.
func readFile(files fs.FS, path string) (string, error) {
	data, err := fs.ReadFile(files, path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w (use a path from list_files)",
			path, err)
	}
	if len(data) > maxRead {
		return string(data[:maxRead]) + "\n(cut off: file too long)", nil
	}
	return string(data), nil
}
