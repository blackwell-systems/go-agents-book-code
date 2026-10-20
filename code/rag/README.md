# Retrieval (RAG), compared

Retrieval-augmented generation in standard-library Go, and a comparison with two other ways of answering questions from a set of documents.
The documents are the 25 Markdown files (about 53 KB) of `shortlink`, a small made-up URL shortener, in `testdata/docs`.

The three approaches, each asked the same 16 questions:

| Approach | How it answers |
| --- | --- |
| `rag` | Retrieval-augmented generation in one call: embed the question, find the four closest chunks, and send them to the model with the question |
| `rag-tool` | Retrieval as a tool: an agent with a `search_docs` tool decides what to search for, and can search again |
| `files` | An agent with `list_files` and `read_file`, which finds and reads the documents itself |

| File | What it holds |
| --- | --- |
| `retrieve.go` | Chunking by heading, `Embed` (OpenRouter's embeddings endpoint, the same key as the chat calls), and `Index`, an in-memory vector search by cosine similarity |
| `answer.go` | The three approaches and their prompts |
| `questions.go` | The questions and the grader: an answer is right when it contains the phrases a right answer must hold |
| `main.go` | The comparison, and `-ask` for one question |
| `agent.go`, `client.go`, `tools.go`, `replay.go` | The agent loop, the API client, the file tools, and the recording, from `firstagent` (the client also counts calls and cost, and embeds) |

## Run it

From the `code` directory, with no API key:

```console
$ go run ./rag
```

The API's replies come from `testdata/run.json`, recorded on October 19, 2026 with `xiaomi/mimo-v2.6-pro` for the answers and `voyageai/voyage-4-lite` for the embeddings; the chunking, the search, and the file tools run for real.
`testdata/out.txt` is the output.

```text
index: 140 chunks, $0.00028 to embed

approach  correct  calls  tokens in      out       cost
rag         16/16     32       8714     3685    $0.0064
rag-tool    16/16     72      25834     6103    $0.0128
files       16/16     54      43201     6098    $0.0151
```

On these documents, all three answered every question, including the last, which the documents don't answer (does shortlink make QR codes?): each said so instead of inventing a feature.
The difference is cost.
`rag` makes two calls per question, one to embed it and one to answer, and cost half as much as `rag-tool` and about 40 percent as much as `files`.
`rag-tool` searched more than once on 13 of the 16 questions (five times on the last), and each search is an embedding call and another turn.
`files` listed the files once per question and chose what to read from their names alone: 1 to 6 files a question, 39 reads in all, and `README.md` only once.
Everything it read stayed in the conversation for the turns after, so it sent 43,201 input tokens, five times as many as `rag`.
Its choices relied on file names that say what each file holds (`docs/rate-limits.md`, `docs/upgrading.md`); this run doesn't test documents with less helpful names.
Calls count the embedding calls as well as the chat calls.

The grader matches phrases.
Its list of ways to say "the documents don't say" was widened after reading the recorded answers, when a right answer ("the docs don't describe any way") was marked wrong; the answers themselves are as recorded.

With `OPENROUTER_API_KEY` set, the comparison runs live (about $0.035), and `-ask` answers one question with one approach:

```console
$ go run ./rag -ask rag "Which release added link expiry?"
```

`OPENROUTER_MODEL` picks another chat model; `-record file` saves a live run's API calls in the format of `testdata/run.json`.

## What it leaves out

The index lives in memory and is rebuilt on every run, which suits a few thousand chunks; a larger set keeps its vectors in a database with a vector index.
There is no keyword search alongside the vector search, no reranking, and no check that an answer's citations hold what it claims.
The grader matches phrases, so it can mark a right answer wrong when the answer is worded differently; read the answers in `testdata/run.json` before trusting a score.
