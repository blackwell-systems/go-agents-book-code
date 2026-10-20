package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
)

// tag::chunk[]

// Chunk is one piece of one document: the unit that is embedded,
// searched, and handed to the model.
type Chunk struct {
	Doc     string `json:"doc"`     // the file it came from
	Heading string `json:"heading"` // the nearest heading above it
	Text    string `json:"text"`
}

// chunkDocs splits every Markdown file under docs into chunks, one per
// section: the text under each heading, with long sections cut at
// paragraph breaks so no chunk passes maxChunk bytes.
func chunkDocs(docs fs.FS, maxChunk int) ([]Chunk, error) {
	var out []Chunk
	err := fs.WalkDir(docs, ".", func(p string, d fs.DirEntry,
		err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		data, err := fs.ReadFile(docs, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		heading, body := "", ""
		flush := func() {
			for _, part := range splitParas(body, maxChunk) {
				out = append(out, Chunk{Doc: p, Heading: heading, Text: part})
			}
			body = ""
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if h, ok := strings.CutPrefix(line, "#"); ok {
				flush()
				heading = strings.TrimSpace(strings.TrimLeft(h, "#"))
				continue
			}
			body += line + "\n"
		}
		flush()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("chunk docs: %w", err)
	}
	return out, nil
}

// end::chunk[]

// splitParas cuts text at blank lines into parts no longer than limit
// bytes, keeping paragraphs whole where it can.
func splitParas(text string, limit int) []string {
	var parts []string
	cur := ""
	for p := range strings.SplitSeq(strings.TrimSpace(text), "\n\n") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if cur != "" && len(cur)+len(p)+2 > limit {
			parts = append(parts, cur)
			cur = ""
		}
		cur = strings.TrimSpace(cur + "\n\n" + p)
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return parts
}

// embedDims is the length of each vector. The embeddings model makes
// 1,024 numbers by default and can make 256 to 2,048; 256 is a
// quarter of the storage and of the work in every comparison.
const embedDims = 256

// tag::embed[]

// Embed turns texts into vectors with an embeddings model, through
// the same API and key as the chat calls.
func (c *Client) Embed(ctx context.Context, model string,
	texts []string) ([][]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model": model, "input": texts, "dimensions": embedDims,
	})
	if err != nil {
		return nil, fmt.Errorf("encode embeddings request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embeddings request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("send embeddings request: %w", err)
	}
	defer resp.Body.Close()
	return c.readEmbeddings(resp, len(texts))
}

// end::embed[]

func (c *Client) readEmbeddings(resp *http.Response,
	n int) ([][]float64, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read embeddings: %w", err)
	}
	var r struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage Usage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	err = json.Unmarshal(data, &r)
	switch {
	case resp.StatusCode != http.StatusOK && r.Error != nil:
		return nil, fmt.Errorf("embeddings API: %s: %s", resp.Status,
			r.Error.Message)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("embeddings API: %s", resp.Status)
	case err != nil:
		return nil, fmt.Errorf("decode embeddings: %w", err)
	case len(r.Data) != n:
		return nil, fmt.Errorf("embeddings API: %d vectors for %d texts",
			len(r.Data), n)
	}
	c.count(r.Usage)
	out := make([][]float64, n)
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= n {
			return nil, fmt.Errorf("embeddings API: index %d", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}

// tag::index[]

// Index holds every chunk with its vector, and finds the chunks
// closest to a question by cosine similarity: an in-memory vector
// search, which for a few thousand chunks needs no database.
type Index struct {
	Chunks  []Chunk
	Vectors [][]float64
}

// Hit is one search result.
type Hit struct {
	Chunk
	Score float64 // cosine similarity, from -1 to 1
}

// Search returns the k chunks most similar to the question's vector,
// best first.
func (ix *Index) Search(q []float64, k int) []Hit {
	hits := make([]Hit, len(ix.Chunks))
	for i, v := range ix.Vectors {
		hits[i] = Hit{ix.Chunks[i], cosine(q, v)}
	}
	slices.SortFunc(hits, func(a, b Hit) int {
		return cmp.Compare(b.Score, a.Score)
	})
	return hits[:min(k, len(hits))]
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range min(len(a), len(b)) {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// end::index[]

// buildIndex chunks the documents and embeds every chunk in one call.
func buildIndex(ctx context.Context, c *Client, model string,
	docs fs.FS) (*Index, error) {
	chunks, err := chunkDocs(docs, 700)
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(chunks))
	for i, ch := range chunks {
		texts[i] = ch.passage()
	}
	vecs, err := c.Embed(ctx, model, texts)
	if err != nil {
		return nil, err
	}
	return &Index{Chunks: chunks, Vectors: vecs}, nil
}

// passage is the chunk as it is embedded and shown to the model: its
// document and heading first, so the text carries its context.
func (ch Chunk) passage() string {
	return fmt.Sprintf("[%s: %s]\n%s", ch.Doc, ch.Heading, ch.Text)
}
