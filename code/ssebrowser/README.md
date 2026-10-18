# Agent progress in a browser

Chapter 6 reads server-sent events from the model's API and turns them into the agent's events.
This example goes the other way: a server written with `net/http` alone streams those events (text, tool calls, tool results, turns, and the end of the run) to a browser page that reads them with `EventSource`.

It shows:

* the response headers an event stream needs, and flushing each event with `http.ResponseController`;
* a write deadline per event, so a client that stops reading is dropped;
* a heartbeat comment while the model is quiet, so proxies keep the connection open;
* a client going away, seen in the request's context, cancelling the run;
* one goroutine per stream, owned by the handler, which cancels it and waits for it before returning;
* a graceful shutdown that ends the open streams, which never go idle on their own.

| File | What |
| --- | --- |
| `server.go` | The handler, the goroutine that runs the agent, and the writes |
| `sse.go` | Each `agent.Event` in the event stream format, with JSON data |
| `index.html` | The page: a task, Run and Stop buttons, and the run as it happens |
| `main.go` | The program: chapter 6's agent on the duration project, and the server's start and shutdown |
| `pace.go`, `script.go` | The scripted model, slowed down so a person can watch it |

## Run it

The model is scripted (chapter 1's run on the duration project), so it runs offline, while the tools, including `go test`, run for real.
The page sends its task, but the scripted model gives the same replies whatever the task is.

```
cd code
go run ./ssebrowser
```

Open the address it prints and press Run.
Press Stop partway through: the page closes its `EventSource`, and the server cancels the run.
To watch the raw stream, use `curl -N 'http://localhost:8080/events?task=fix'`.

```
go test -race ./ssebrowser
```

The tests read the stream from an `httptest` server and check the headers, every event, the heartbeat, that a client leaving cancels the run, and that shutdown ends open streams.

## What a real service adds

* The page shows a run's error text as it is; a public service would send a short stop reason instead.
* Each request starts a run; a real service limits how many run at once, and per user.
* A dropped connection loses the run. To let a client reconnect and continue, give each event an `id:` line, keep the run going apart from the request, and resume from the `Last-Event-ID` header the browser sends.
