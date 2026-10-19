# A first agent, start to finish

The smallest complete agent: it answers a question about the files in one directory, reading them with two tools, in standard-library Go.
It is written to be read from top to bottom by someone building their first agent, and it imports nothing from the chapters, so its files can be copied into a new module as they are.

| File | What it holds |
| --- | --- |
| `agent.go` | The types (`Message`, `ToolCall`, `Tool`) and `Run`, the loop: call the model, run the tools it asks for, repeat until it answers |
| `client.go` | A client for an OpenAI-compatible chat completions API, such as OpenRouter's |
| `tools.go` | `list_files` and `read_file`, which read only inside the directory they're given, through `os.Root` |
| `main.go` | The system prompt, the flags, and the setup |
| `replay.go` | The recorded run that lets the program work without a key, and the recorder that made it |

## Run it

From the `code` directory, with no API key:

```console
$ go run ./firstagent
```

The model's replies come from `testdata/run.json`, recorded from `xiaomi/mimo-v2.5-pro` on October 19, 2026 (three calls, $0.0006); the tools read the sample project in `testdata/project` for real.
`testdata/out.txt` is the output.

With `OPENROUTER_API_KEY` set, the program calls the live model, and you can point it at your own files and ask your own question:

```console
$ go run ./firstagent -dir ~/notes "What did I decide about the cache?"
```

`OPENROUTER_MODEL` picks another model; `-record file` saves a live run's API calls in the same format as `testdata/run.json`.

## What it leaves out

The book's agent adds what this one skips: retries for failed calls, a dollar budget, recovering a tool that panics, streaming, context management for long runs, and approval before actions that change things.
These tools only read, which is what makes it safe to leave them out here.
