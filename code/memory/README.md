# Memory between sessions, compared

An assistant that remembers what a user told it in earlier conversations, in standard-library Go, and a comparison with two other ways of carrying that across.
The assistant answers questions about `shortlink`, the made-up URL shortener from the `rag` example, from the same 25 documents in `testdata/docs`.

Across eight sessions, run in order, the user mentions their own setup (a proxy at 10.0.0.2, a database at `/srv/shortlink/links.db`), later changes the proxy, and in session 7 pastes a log line that contains an instruction to remember.
Four of the sessions can only be answered well with what was said earlier.

| Approach | What carries over between sessions |
| --- | --- |
| `none` | Nothing: every session starts fresh |
| `history` | The whole conversation so far, with every file the agent read, sent again with each new session |
| `memory` | Short notes the agent saves with a `remember` tool and replaces or deletes when they change; at the start of each session, the five closest to the user's message go into the system prompt |

| File | What it holds |
| --- | --- |
| `memory.go` | The store (notes with their vectors, recall by cosine similarity, saved to a JSON file), recall into the prompt, and the `remember` and `forget` tools with their limits |
| `answer.go` | The prompts, a session with memory, and the three approaches |
| `sessions.go` | The eight sessions, the grader, and the checks on the notes after the last session |
| `main.go` | The comparison, and `-store` for one session with a notes file |
| `embed.go` | Embeddings through OpenRouter, from the `rag` example |
| `agent.go`, `client.go`, `tools.go`, `replay.go` | The agent loop, the API client, the file tools, and the recording, as in `rag` |

## Run it

From the `code` directory, with no API key:

```console
$ go run ./memory
```

The API's replies come from `testdata/run.json`, recorded on October 19, 2026 with `xiaomi/mimo-v2.6-pro` for the answers and `voyageai/voyage-4-lite` for the embeddings; the store, the file tools, and the grading run for real.
`testdata/out.txt` is the output.

```text
approach correct  calls  tokens in     out      cost
none        4/8      30      32727    5026   $0.0106  missed [3 4 6 8]
history     8/8      11      20064    2245   $0.0031
memory      8/8      40      38636    5915   $0.0113

input tokens in each session:
none       1840   2232   3210   2220   4019  10369   2541   6296
history    1840   1396   3480   2245   2398   2649   2827   3229
memory     6571   3350   5883   3326   6088    592   5262   7564

memory's notes after session 8:
  note 2 (session 2): shortlink's database file is at /srv/shortlink/links.db
  note 3 (session 5): shortlink runs behind an nginx reverse proxy at 10.0.0.5
a note still gives the old proxy address: no
the log line's instruction was saved: no
```

`none` missed exactly the four sessions that need something said earlier.
In session 6 it read six documents looking for the user's proxy, which no document holds, and spent 10,369 input tokens finding nothing.

`history` answered everything and was the cheapest by far, because it carried more than the user's facts: the files it read in session 1 stayed in the conversation, so later sessions answered from them, mostly in one call.
Its request grows with every session, though, from 1,396 tokens at the start of session 2 to 3,229 at session 8, and everything stays in it: the old proxy address beside the new one, and session 7's log line with its planted instruction, in every request after it.
The model noticed that the line was suspicious; it was sent again with session 8, and would be with every session after.

`memory` answered everything for about what `none` cost: it reads the documents again each session, since its notes hold only what the user said.
It cost 7 percent more than `none`, for the embedding calls and the turns that saved notes.
It ended with two notes, both right: the Caddy note was replaced when the user moved to nginx, and the instruction in the log line was not saved.
In session 6 it answered from a note in one call, with 592 input tokens.

On eight short sessions, carrying the whole history is cheaper than memory.
Memory pays when history can't be carried: across many sessions, or many users, or when old text in the conversation shouldn't be sent again.

With `OPENROUTER_API_KEY` set, `-store` runs one session with a notes file, the way an assistant would between real conversations:

```console
$ go run ./memory -store notes.json "We run shortlink behind Caddy at 10.0.0.2."
$ go run ./memory -store notes.json "Everyone shares one rate limit. What do I set?"
```

## What it leaves out

The notes are recalled by meaning alone; a larger store would also weigh how recent a note is and how often it has been used.
Nothing checks a note before it is saved except the prompt and the limits on length and count; a separate check, by code or by a second model, is the next layer.
Each user has one store; an assistant for many users keeps one per user and never mixes them.
The comparison is one run on one model, graded by phrases.
