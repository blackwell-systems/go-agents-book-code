// Command sample sends one question to a model through OpenRouter and
// prints the answer. It backs the sample page used to tune the book's
// layout.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

const (
	defaultBaseURL = "https://openrouter.ai/api/v1"
	defaultModel   = "anthropic/claude-haiku-5.5"
)

// recorded is a saved reply in OpenRouter's response format. With no
// API key set, the program answers from it so it runs without an
// account.
//
//go:embed testdata/response.json
var recorded []byte

// tag::types[]
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []message `json:"messages"`
}

type response struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
}

// end::types[]

// reply is the model's answer and why it stopped. A FinishReason other
// than "stop" is not an error: the caller decides what to do with it.
type reply struct {
	Text         string
	FinishReason string
}

type client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// replay is an http.RoundTripper that answers every request with the
// same recorded body instead of calling the network.
type replay struct{ body []byte }

func (r replay) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(r.body)),
		Request:    req,
	}, nil
}

func main() {
	c := &client{
		baseURL:    envOr("OPENROUTER_BASE_URL", defaultBaseURL),
		apiKey:     os.Getenv("OPENROUTER_API_KEY"),
		model:      envOr("OPENROUTER_MODEL", defaultModel),
		httpClient: http.DefaultClient,
	}
	if c.apiKey == "" {
		fmt.Fprintln(os.Stderr,
			"OPENROUTER_API_KEY not set: replaying a recorded reply")
		c.httpClient = &http.Client{Transport: replay{body: recorded}}
	}
	r, err := c.ask(context.Background(),
		"In one sentence, what is a language model?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(r.Text)
	if r.FinishReason != "stop" {
		fmt.Fprintf(os.Stderr, "note: reply ended early (%s)\n",
			r.FinishReason)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (c *client) ask(ctx context.Context, question string) (reply, error) {
	// tag::send[]
	body, err := json.Marshal(request{
		Model:     c.model, // <1>
		MaxTokens: 1024,
		Messages: []message{
			{Role: "user", Content: question},
		},
	})
	if err != nil {
		return reply{}, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body)) // <2>
	if err != nil {
		return reply{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey) // <3>
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return reply{}, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	// end::send[]

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return reply{}, fmt.Errorf("model API: %s: %s", resp.Status, msg)
	}

	// tag::read[]
	var out response
	if err := json.UnmarshalRead(resp.Body, &out); err != nil {
		return reply{}, fmt.Errorf("decode reply: %w", err)
	}
	if len(out.Choices) == 0 {
		return reply{}, errors.New("decode reply: no choices")
	}
	choice := out.Choices[0] // <1>
	return reply{
		Text:         choice.Message.Content,
		FinishReason: choice.FinishReason, // <2>
	}, nil
	// end::read[]
}
