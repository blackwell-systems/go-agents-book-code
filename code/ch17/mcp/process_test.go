package mcp

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain runs the test binary as an MCP server on its standard input
// and output when MCP_TEST_SERVER is set, so the tests below can start a
// real server process: the test binary itself.
func TestMain(m *testing.M) {
	if os.Getenv("MCP_TEST_SERVER") == "" {
		os.Exit(m.Run())
	}
	s := testServer()
	s.Log = log.New(os.Stderr, "server: ", 0)
	s.Tools = append(s.Tools,
		Tool{Name: "env", Call: func(context.Context, jsontext.Value) (string, error) {
			return fmt.Sprintf("key set: %v", os.Getenv("OPENROUTER_API_KEY") != ""), nil
		}},
		Tool{Name: "crash", Call: func(context.Context, jsontext.Value) (string, error) {
			fmt.Fprintln(os.Stderr, "server: out of memory, giving up")
			os.Exit(3)
			return "", nil
		}})
	s.Log.Print("ready")
	if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		s.Log.Fatal(err)
	}
	s.Log.Print("input closed, exiting")
}

// start starts the test binary as a server.
func start(ctx context.Context, t *testing.T) *Client {
	t.Helper()
	var log strings.Builder
	c, err := Start(ctx, Command{Path: os.Args[0],
		Env: BaseEnv("MCP_TEST_SERVER=1"), Log: &log},
		Implementation{Name: "test client", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestStartEnv checks that a server process gets only the environment
// it is given: not the API key in this process's.
func TestStartEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-not-a-real-key")
	c := start(t.Context(), t)
	defer c.Close()
	r, err := c.CallTool(t.Context(), "env", nil)
	if err != nil || r.Text() != "key set: false" {
		t.Errorf("env = %q, %v", r.Text(), err)
	}
}

// TestServerExits checks that a server that dies in the middle of a
// call fails the call with ErrServerGone, quoting its last words.
func TestServerExits(t *testing.T) {
	c := start(t.Context(), t)
	defer c.Close()
	_, err := c.CallTool(t.Context(), "crash", nil)
	if !errors.Is(err, ErrServerGone) || !strings.Contains(err.Error(), "out of memory") {
		t.Errorf("crash: %v", err)
	}
	if _, err := c.CallTool(t.Context(), "echo", nil); !errors.Is(err, ErrServerGone) {
		t.Errorf("a call after the crash: %v", err)
	}
}

// TestClose checks that Close stops the server by closing its input,
// without having to kill it, and that the context does the same.
func TestClose(t *testing.T) {
	c := start(t.Context(), t)
	began := time.Now()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took >= shutdownGrace {
		t.Errorf("Close took %v: the server was killed, not stopped", took)
	}
	if st := c.proc.ProcessState; st == nil || !st.Success() {
		t.Errorf("server exit: %v", st)
	}

	ctx, cancel := context.WithCancel(t.Context())
	c = start(ctx, t)
	cancel()
	select {
	case <-c.done:
	case <-time.After(shutdownGrace):
		t.Fatal("the server didn't stop when the context ended")
	}
}
