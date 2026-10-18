package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAsk(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    reply
	}{
		{
			name:    "complete answer",
			fixture: "testdata/response.json",
			want: reply{
				Text:         "A language model predicts the next token of text.",
				FinishReason: "stop",
			},
		},
		{
			name:    "cut off by max tokens",
			fixture: "testdata/response-length.json",
			want:    reply{Text: "A language model", FinishReason: "length"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, err := os.ReadFile(tt.fixture)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/chat/completions" {
						t.Errorf("path = %q, want /chat/completions", r.URL.Path)
					}
					if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
						t.Errorf("Authorization = %q, want Bearer test-key", got)
					}
					if _, err := w.Write(fixture); err != nil {
						t.Error(err)
					}
				}))
			defer srv.Close()

			c := &client{
				baseURL:    srv.URL,
				apiKey:     "test-key",
				model:      "test-model",
				httpClient: srv.Client(),
			}
			got, err := c.ask(t.Context(), "What is a model?")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ask = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReplay(t *testing.T) {
	c := &client{
		baseURL:    defaultBaseURL,
		model:      defaultModel,
		httpClient: &http.Client{Transport: replay{body: recorded}},
	}
	got, err := c.ask(t.Context(), "What is a model?")
	if err != nil {
		t.Fatal(err)
	}
	if got.FinishReason != "stop" || got.Text == "" {
		t.Errorf("ask = %+v, want a complete answer", got)
	}
}
