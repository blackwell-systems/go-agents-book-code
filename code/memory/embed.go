package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// embedDims is the length of each vector; the embeddings model can make
// 256 to 2,048 numbers, and a few hundred notes need no more than 256.
const embedDims = 256

// Embed turns texts into vectors with an embeddings model, through the
// same API and key as the chat calls.
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

// cosine is the cosine similarity of two vectors: 1 when they point
// the same way, near 0 when they are unrelated.
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
