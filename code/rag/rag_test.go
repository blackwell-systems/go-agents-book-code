package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

var update = flag.Bool("update", false, "rewrite testdata/out.txt")

func TestChunkDocs(t *testing.T) {
	docs := fstest.MapFS{
		"a.md": {Data: []byte("# Title\n\nIntro.\n\n## Part one\n\n" +
			"First.\n\nSecond.\n\n## Part two\n\nThird.\n")},
		"skip.txt": {Data: []byte("not Markdown")},
	}
	chunks, err := chunkDocs(docs, 700)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range chunks {
		text := strings.ReplaceAll(c.Text, "\n\n", " / ")
		got = append(got, c.Heading+": "+text)
	}
	want := []string{"Title: Intro.", "Part one: First. / Second.",
		"Part two: Third."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("chunks = %q, want %q", got, want)
	}
}

func TestSplitParas(t *testing.T) {
	parts := splitParas("aaaa\n\nbbbb\n\ncccc", 10)
	want := []string{"aaaa\n\nbbbb", "cccc"}
	if !slices.Equal(parts, want) {
		t.Errorf("parts = %q", parts)
	}
}

func TestSearch(t *testing.T) {
	ix := &Index{
		Chunks: []Chunk{{Text: "east"}, {Text: "north"},
			{Text: "north-east"}},
		Vectors: [][]float64{{1, 0}, {0, 1}, {1, 1}},
	}
	hits := ix.Search([]float64{0.1, 1}, 2)
	if len(hits) != 2 || hits[0].Text != "north" ||
		hits[1].Text != "north-east" {
		t.Errorf("hits = %+v", hits)
	}
	if cosine([]float64{0, 0}, []float64{1, 0}) != 0 {
		t.Error("cosine of a zero vector should be 0")
	}
}

func TestGrade(t *testing.T) {
	q := question{Want: [][]string{{"SQLite"}, {"shortlink.db"}}}
	tests := []struct {
		answer string
		want   bool
	}{
		{"It uses sqlite, in shortlink.db (README.md).", true},
		{"It uses SQLite.", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := grade(q, tt.answer); got != tt.want {
			t.Errorf("grade(%q) = %v, want %v", tt.answer, got, tt.want)
		}
	}
}

func TestEmbedErrors(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		want       string
	}{
		{"401", `{"error": {"message": "User not found."}}`, 401,
			"User not found."},
		{"too few vectors", `{"data": [{"index": 0, "embedding": [1]}]}`, 200,
			"1 vectors for 2 texts"},
		{"bad index", `{"data": [{"index": 5, "embedding": [1]},
			{"index": 0, "embedding": [1]}]}`, 200, "index 5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.body)
				}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL}
			_, err := c.Embed(context.Background(), "m", []string{"a", "b"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestAskNeedsKey(t *testing.T) {
	noKey := func(string) string { return "" }
	err := run(context.Background(), noKey, "rag", "Why?", "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Errorf("err = %v", err)
	}
}

// TestCompare replays the recorded comparison, as `go run ./rag` does
// with no key: recorded model replies and embeddings, and the real
// chunking, search, and file tools. testdata/out.txt is the output;
// -update rewrites it.
func TestCompare(t *testing.T) {
	var out bytes.Buffer
	noKey := func(string) string { return "" }
	err := run(context.Background(), noKey, "", "", "", &out)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "out.txt")
	if *update {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("output differs from %s:\n%s", golden, out.String())
	}
}
