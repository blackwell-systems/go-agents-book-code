package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

const topK = 4

// tag::rag[]

const ragPrompt = `Answer the question using only the passages from
shortlink's documentation that come with it. Name the document each
fact comes from. If the passages don't answer the question, say that
the documentation doesn't say.`

// askRAG is retrieval-augmented generation in one call: embed the
// question, find the closest chunks, and send them with the question.
func askRAG(ctx context.Context, c *Client, ix *Index, embedModel,
	question string) (string, error) {
	qv, err := c.Embed(ctx, embedModel, []string{question})
	if err != nil {
		return "", err
	}
	var passages []string
	for _, h := range ix.Search(qv[0], topK) {
		passages = append(passages, h.passage())
	}
	msgs := []Message{
		{Role: "system", Content: ragPrompt},
		{Role: "user", Content: "Passages:\n\n" +
			strings.Join(passages, "\n\n") + "\n\nQuestion: " + question},
	}
	reply, err := c.Chat(ctx, msgs, nil)
	if err != nil {
		return "", err
	}
	if reply.FinishReason == "length" {
		return "", errCutOff
	}
	return reply.Message.Content, nil
}

// end::rag[]

// tag::search-tool[]

const toolPrompt = `You answer questions about shortlink from its
documentation. Use search_docs to find passages; if the first results
don't answer the question, search again with other words. Answer only
from what you found, name the documents you used, and if the
documentation doesn't answer the question, say so.`

// searchTool is retrieval as a tool: the model decides when to search
// and what to search for, and can search again.
func searchTool(c *Client, ix *Index, embedModel string) Tool {
	return Tool{
		Name: "search_docs",
		Description: "Search shortlink's documentation. Returns the " +
			"passages closest in meaning to the query, best first.",
		Parameters: []byte(`{"type": "object", "properties": {"query":
			{"type": "string"}}, "required": ["query"]}`),
		Run: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("arguments: %w", err)
			}
			qv, err := c.Embed(ctx, embedModel, []string{a.Query})
			if err != nil {
				return "", err
			}
			var out []string
			for _, h := range ix.Search(qv[0], topK) {
				out = append(out, h.passage())
			}
			return strings.Join(out, "\n\n"), nil
		},
	}
}

// end::search-tool[]

const filesPrompt = `You answer questions about shortlink from its
documentation, a directory of Markdown files. Use list_files to see
the files and read_file to read them. Answer only from what the files
say, name the files you used, and if they don't answer the question,
say so.`

// askAgent runs a tool-using agent with the given prompt and tools.
func askAgent(ctx context.Context, c *Client, prompt string,
	tools []Tool, question string) (string, error) {
	msgs := []Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: question},
	}
	answer, _, err := Run(ctx, c, tools, msgs, 8, io.Discard)
	return answer, err
}

// approach is one way of answering a question from the documents.
type approach struct {
	Name string
	Ask  func(ctx context.Context, question string) (string, error)
}

func approaches(c *Client, ix *Index, embedModel string,
	docs fs.FS) []approach {
	return []approach{
		{"rag", func(ctx context.Context, q string) (string, error) {
			return askRAG(ctx, c, ix, embedModel, q)
		}},
		{"rag-tool", func(ctx context.Context, q string) (string, error) {
			return askAgent(ctx, c, toolPrompt,
				[]Tool{searchTool(c, ix, embedModel)}, q)
		}},
		{"files", func(ctx context.Context, q string) (string, error) {
			return askAgent(ctx, c, filesPrompt, fileTools(docs), q)
		}},
	}
}
