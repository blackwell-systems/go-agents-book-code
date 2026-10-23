# Building AI Agents in Go: companion code

This is the code from the book [*Building AI Agents in Go*](https://golang-agents.com) by Dayna Blackwell.
Each chapter's code is a full snapshot in `code/chNN/`, so you can run any chapter without the ones before it.
The book's listings are excerpts from these files, and every excerpt comes from code that these tests exercise.

## What you need

* Go 1.27 or later (`go version`).
  The code uses `encoding/json/v2`, which is in the standard library from Go 1.27.
* Nothing else for the first ten chapters: the code uses only the standard library.
  Later chapters name their own dependencies, and `code/ch10/sdk`, `code/ch12/otel`, and `code/ch16` are separate modules with their own `go.mod`.

## Run a chapter offline

Every example runs without a key.
With no `OPENROUTER_API_KEY` set, a program replays a saved model run (the files under `testdata/replies`) while its tools still run for real.

```
cd code
go run ./ch01
go run ./ch04
go test ./ch04/...
```

To check everything at once (several minutes; the provider replays in chapters 12 to 15 are the slowest):

```
cd code
go vet ./...
go test -race -p 4 -timeout 30m ./...
```

The practice service `code/remind` and the crash catalog `code/catalog` have tests of their own.
`remind` is a separate module, so run `cd code/remind && go test ./...`.
Its bugs are deliberate: the agent's job in Part 2 is to triage crashes they cause, so do not fix them.

## Print the book's results again

Every table of measured results in the book comes from live runs saved under `code/evidence/`.
This prints them all again from those recordings, offline:

```
cd code
go run ./evidence
```

## Run against a live model

The examples that call a model use OpenRouter's chat completions endpoint.

```
export OPENROUTER_API_KEY=...        # from https://openrouter.ai
export OPENROUTER_MODEL=anthropic/claude-haiku-5.5   # optional
cd code
go run ./ch04
```

The model ID is one configuration value, and any tool-capable model on OpenRouter should work.
Live runs cost money; the book states what each one cost when it was recorded, and prices change.
Set a spending limit on your OpenRouter key before you run the larger evaluations (chapters 13 and 17).
Model replies vary from run to run, so a live run will not match the book's output exactly.

## Extra examples

Five small programs go beyond the chapters, tested like the rest. The first two build on chapter 6's agent types; the last three stand alone:

* `code/shapes/`: the workflow shapes from chapter 5's table, written out: a fixed chain with checks between steps, a router that uses a small model to pick a handler, parallel calls with a vote, and check and revise.
* `code/ssebrowser/`: an agent's progress served to a browser with server-sent events, using `net/http` alone, with heartbeats, flushing, and cancellation when the client leaves.
* `code/firstagent/`: the smallest complete agent, which answers a question about the files in a directory with two read-only tools. It imports nothing from the chapters, so its files can start a module of your own; golang-agents.com walks through it.
* `code/rag/`: retrieval-augmented generation (RAG) with the standard library: documents split into chunks, embedded through OpenRouter, and searched in memory. It answers questions about a small service's documentation three ways, RAG in one call, search as a tool, and an agent that reads the files, and compares them. The book doesn't cover retrieval; golang-agents.com does.
* `code/memory/`: memory between sessions: notes an agent saves with a tool, replaces when they change, and recalls by meaning, compared over eight sessions with carrying nothing and with carrying the whole conversation. The book doesn't cover memory across sessions; golang-agents.com does.
* `code/identity/`: an agent's three identities, the user it acts for, the agent, and the run, carried by a short-lived, scoped credential: the harness mints it, a tool's transport attaches it so the model never sees it, a service verifies it and checks its scope, a sub-agent gets a narrower one, and every attempt is audited. It needs no model and no API key. The book doesn't cover agent identity; golang-agents.com does.

All five run offline:

```
cd code
go run ./shapes
go run ./ssebrowser   # then open the address it prints
go run ./firstagent
go run ./rag
go run ./memory
go run ./identity
go test -race ./shapes ./ssebrowser ./firstagent ./rag ./memory ./identity
```

Each directory's `README.md` says more.

## Layout

| Path | What |
| --- | --- |
| `code/chNN/` | Chapter NN's code, a full snapshot with `main.go` and the agent as package `agent` |
| `code/remind/` | The practice service with seeded bugs (its own module) |
| `code/catalog/` | The crash catalog and its grader |
| `code/tracker/` | The small issue tracker the agent files issues in |
| `code/sample/` | The sample program |
| `code/shapes/` | The workflow shapes: chain, router, vote, check and revise |
| `code/ssebrowser/` | An agent's events streamed to a browser page |
| `code/firstagent/` | A standalone first agent: files in a directory, two tools |
| `code/rag/` | Retrieval (RAG) compared with search as a tool and with reading the files |
| `code/memory/` | Memory between sessions compared with no memory and with the whole history |
| `code/identity/` | Scoped, short-lived credentials for an agent, its tools, and its sub-agents, with an audit log |

## License

The code is licensed under the Apache License, Version 2.0; see `LICENSE`.
The book's text and art are not part of this repository and are not covered by this license.

## The book

*Building AI Agents in Go* explains every line of this code, and every measurement it produced: https://golang-agents.com
