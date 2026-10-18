# Workflow shapes

Chapter 5's table "Shapes for programs that use models" names the shapes a program can take before it becomes an agent.
The book builds the agent; this example builds the shapes above it in the table, plus the evaluator-optimizer shape the table leaves out.
The names follow Anthropic's "Building effective agents" (prompt chaining, routing, parallelization, evaluator-optimizer), which the table is adapted from.

| Shape | File | What it does |
| --- | --- | --- |
| A fixed chain | `chain.go` | `Chain` runs steps in order, each with the last one's output, and checks each reply in code before the next step sees it |
| Routing | `route.go` | `Router.Route` asks a small, cheap model for the input's kind and runs that kind's handler: a large model, a small one, or plain code |
| Parallel calls with a vote | `vote.go` | `Vote` asks several voters at once and returns the first answer a quorum agrees on; the rest of the calls are cancelled |
| Check and revise | `revise.go` | `Revise` drafts, has a `Critic` judge the draft, and sends the notes back for a new draft, up to a limit on revisions |

Each function's doc comment says when to use the shape and what it costs.
All four use chapter 6's `agent.Model`, so any model from the book works with them, including chapter 6's OpenRouter client.
A reply the model didn't finish (`finish_reason` of `length`, `content_filter`, or `error`) is an error, never an answer, as in chapter 5's hardened loop.

## Run it

Everything runs offline on scripted models, which answer each call in order from a list (`script.go`), the same idea as chapter 1's `Fake`.

```
cd code
go run ./shapes
go test -race ./shapes
```

`go run` prints what each shape returned and how many calls it made, on a crash report from the `remind` service.
`go test ./shapes -run TestRun -update` rewrites `testdata/out.txt` after you change the demos.
