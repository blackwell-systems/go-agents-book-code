// Command ssebrowser serves an agent's progress to a browser with
// server-sent events, using only net/http. Open the page it prints,
// press Run, and watch chapter 6's agent fix the duration project: the
// model is scripted, so it runs offline, while the tools, including go
// test, run for real.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

//go:embed testdata/duration
var project embed.FS

func main() {
	addr := flag.String("addr", "localhost:8080",
		"the address to listen on")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()
	if err := serve(ctx, *addr, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// tag::serve[]

// serve runs the server until ctx is done, then shuts it down.
func serve(ctx context.Context, addr string, w io.Writer) error {
	s := &Server{
		Run:       demo(700*time.Millisecond, 40*time.Millisecond),
		Heartbeat: 15 * time.Second,
		WriteWait: 10 * time.Second,
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second, // <1>
		BaseContext: func(net.Listener) context.Context {
			return ctx // <2>
		},
	}
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	_, err = fmt.Fprintf(w, "open http://%s\n", ln.Addr())
	if err != nil {
		return fmt.Errorf("print address: %w", err)
	}
	// end::serve[]
	// tag::serve-stop[]
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }() // <1>
	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(),
		5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil { // <2>
		return fmt.Errorf("shut down: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// end::serve-stop[]

// demo returns a Runner for chapter 6's agent on a fresh copy of the
// duration project, with a scripted model paced so a person can watch.
func demo(think, word time.Duration) Runner {
	return func(ctx context.Context,
		task string) iter.Seq[agent.Event] {
		m := paced{m: agent.Streamed(&agent.Fake{Script: script}),
			think: think, word: word}
		msgs := []agent.Message{{Role: "user", Content: task}}
		lim := agent.Limits{MaxTurns: 10}
		return agent.Events(ctx, m, agent.Tools(files()), msgs, lim)
	}
}

// files returns the duration project, with its bug.
func files() agent.FS {
	const dir = "testdata/duration"
	entries, err := fs.ReadDir(project, dir)
	if err != nil {
		panic(err) // embedded at build time, so a programmer error
	}
	files := agent.FS{}
	for _, e := range entries {
		b, err := fs.ReadFile(project, path.Join(dir, e.Name()))
		if err != nil {
			panic(err)
		}
		files[e.Name()] = string(b)
	}
	return files
}
