package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

//go:embed index.html
var page []byte

// tag::server[]

// A Runner starts an agent run on task and returns its events, as
// chapter 6's agent.Events does. Breaking out of the range loop stops
// the run.
type Runner func(ctx context.Context, task string) iter.Seq[agent.Event]

// Server serves a page that runs the agent, and the stream of events
// the page reads while the run goes on.
type Server struct {
	Run       Runner
	Heartbeat time.Duration // how often a quiet stream sends a comment
	WriteWait time.Duration // how long a client may take to read one
}

// Handler returns the server's routes: the page at / and the event
// stream at /events?task=....
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter,
		_ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page) //nolint:errcheck // the client may have gone
	})
	mux.HandleFunc("GET /events", s.events)
	return mux
}

// end::server[]

// tag::events[]

// events streams one run's events to the client. The task comes in the
// query string, since a browser's EventSource can only send a GET.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	task := r.URL.Query().Get("task")
	if task == "" {
		http.Error(w, "missing task", http.StatusBadRequest)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream") // <1>
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // <2>
	st := &stream{w: w, rc: http.NewResponseController(w),
		wait: s.WriteWait}
	ctx, cancel := context.WithCancel(r.Context()) // <3>
	events := s.start(ctx, task)
	defer func() {
		cancel()
		for range events { // <4>
		}
	}()
	if err := s.pump(st, events); err != nil {
		slog.DebugContext(ctx, "event stream ended", "err", err)
	}
}

// end::events[]

// tag::pump[]

// pump sends events to the client as they arrive, and a heartbeat when
// none has arrived for a while, until the run stops or a send fails.
func (s *Server) pump(st *stream, events <-chan agent.Event) error {
	if err := st.send(": run started\n\n"); err != nil { // <1>
		return err
	}
	tick := time.NewTicker(s.Heartbeat)
	defer tick.Stop()
	for {
		var msg string
		select {
		case ev, ok := <-events:
			if !ok {
				return nil // the run has stopped
			}
			var err error
			if msg, err = encode(ev); err != nil {
				return err
			}
		case <-tick.C:
			msg = ": heartbeat\n\n" // <2>
		}
		if err := st.send(msg); err != nil {
			return err
		}
	}
}

// end::pump[]

// tag::start[]

// start runs the agent in a goroutine and sends its events on the
// channel it returns. The goroutine stops when ctx is done and closes
// the channel as it returns, so a caller that cancels ctx and then
// reads the channel until it closes knows the run is over.
//
// The goroutine is there so the handler can send heartbeats while the
// model thinks. Only the handler writes to the client, since a
// ResponseWriter isn't safe for concurrent use.
func (s *Server) start(ctx context.Context,
	task string) <-chan agent.Event {
	events := make(chan agent.Event)
	go func() {
		defer close(events)
		for ev := range s.Run(ctx, task) {
			select {
			case events <- ev:
			case <-ctx.Done():
				return // <1>
			}
		}
	}()
	return events
}

// end::start[]

// tag::send[]

// stream writes an event stream to one client.
type stream struct {
	w    io.Writer
	rc   *http.ResponseController
	wait time.Duration
}

// send writes msg and flushes it to the client at once. A client that
// doesn't take it within s.wait is dropped: the write fails, and the
// handler stops.
func (s *stream) send(msg string) error {
	err := s.rc.SetWriteDeadline(time.Now().Add(s.wait)) // <1>
	if err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := io.WriteString(s.w, msg); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	if err := s.rc.Flush(); err != nil { // <2>
		return fmt.Errorf("flush event: %w", err)
	}
	return nil
}

// end::send[]
