package main

import (
	"cmp"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// tag::wrapped[]

// wrapped holds what chapter 12 puts around the agent's calls: the
// retry policies, a recorder for traces, a meter for cost, and a logger.
type wrapped struct {
	policy   agent.Policy    // the model's
	tools    agent.Policy    // the tools', which shares none of the limits
	rec      *agent.Recorder // nil unless -trace
	meter    *agent.Meter
	log      *slog.Logger
	provider string // the host's name, for gen_ai.provider.name
	model    string
	outer    []agent.ToolWare // outermost: egress, the gate (chapter 14)
}

// end::wrapped[]

// newWrapped builds the wrappers for one run, with retries printed as
// they happen and a 429's delay applied to pace, if there is one.
func newWrapped(c config, provider, model string, p *printer,
	pace *agent.Limiter) *wrapped {
	w := &wrapped{meter: &agent.Meter{}, provider: provider,
		model: model, log: slog.New(slog.DiscardHandler)}
	if c.trace {
		w.rec = agent.NewRecorder()
	}
	if c.log {
		w.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
		if w.rec != nil {
			w.log = w.log.With("trace", w.rec.Trace)
		}
	}
	w.policy = agent.Policy{
		Backoff:     c.backoff,
		RetryBudget: &agent.RetryBudget{},
		Breaker:     &agent.Breaker{},
		OnRetry: func(n int, err error, d agent.Decision,
			wait time.Duration) {
			p.printf("  (retry %d in %v, %s: %v)\n", n,
				wait.Round(time.Millisecond), d.Why, err)
			if d.After > 0 && pace != nil {
				pace.Pause(d.After) // the whole account waits
			}
		},
	}
	if pace != nil {
		w.policy.Wait = pace.Wait // before each attempt's clock
	}
	w.tools = agent.Policy{Backoff: c.backoff,
		OnRetry: func(n int, err error, d agent.Decision,
			wait time.Duration) {
			p.printf("  (tool retry %d in %v, %s: %v)\n", n,
				wait.Round(time.Millisecond), d.Why, err)
		}}
	return w
}

// tag::wrap-order[]

// Model wraps m: the chat span outside the retries, then the retries,
// then, for each attempt, its own span, the log line, and the meter.
func (w *wrapped) Model(m agent.StreamModel) agent.StreamModel {
	return agent.WrapModel(m,
		agent.Traced(w.rec, w.provider, w.model),
		agent.Retrying(w.policy),
		agent.TracedAttempts(w.rec),
		agent.Logged(w.log, w.model),
		w.meter.Wrap)
}

// Tools wraps tools: chapter 14's egress check and approval gate
// outermost, then the span, the log line, retries for the tools safe to
// run twice, and each tool's Timeout. Tool retries have a policy of
// their own: a flaky tool says nothing about the provider.
func (w *wrapped) Tools(tools []agent.Tool) []agent.Tool {
	logs := agent.LogOptions{Redact: triage.Redact}
	tools = agent.WrapTools(tools,
		agent.TracedTools(w.rec),
		agent.LoggedTools(w.log, logs),
		agent.RetryTools(w.tools),
		agent.Timeouts)
	return agent.WrapTools(tools, w.outer...) // chapter 14
}

// end::wrap-order[]

// report prints the trace and the meter's breakdown, when asked for.
func (w *wrapped) report(c config, out io.Writer, p *printer) {
	if w.rec != nil {
		p.printf("\ntrace %s\n%s", w.rec.Trace, w.rec.String())
	}
	if c.meter {
		p.printf("\ncost by agent and call\n")
		if err := w.meter.Report(out); err != nil {
			p.printf("%v\n", err)
		}
	}
}

// hostName is the provider a run's calls go to, for traces: the host,
// whichever client is used, as chapter 11 named reasoning's source.
func hostName(c config, direct bool) string {
	if c.provider == "anthropic" && direct {
		return "anthropic"
	}
	return "openrouter"
}

// parseFaults reads -faults, a comma-separated list of what to do to
// each request in turn, or nothing for "".
func parseFaults(s string) ([]agent.Fault, error) {
	if s == "" {
		return nil, nil
	}
	var fs []agent.Fault
	for f := range strings.SplitSeq(s, ",") {
		fault, err := parseFault(f)
		if err != nil {
			return nil, fmt.Errorf("-faults: %w", err)
		}
		fs = append(fs, fault)
	}
	return fs, nil
}

// tag::faults[]

// parseFault reads one item of -faults: a status such as 429 or 503
// (with ":seconds" for a Retry-After), "drop", "lost", "reset:bytes",
// "truncate:bytes", or "ok" to send the request on.
func parseFault(f string) (agent.Fault, error) {
	kind, arg, _ := strings.Cut(strings.TrimSpace(f), ":")
	n, err := strconv.Atoi(cmp.Or(arg, "1"))
	if err != nil || n < 1 {
		return agent.Fault{}, fmt.Errorf("%q: not a count", f)
	}
	switch kind {
	case "ok":
		return agent.Fault{}, nil
	case "drop":
		return agent.Fault{Drop: true}, nil
	case "lost":
		return agent.Fault{Lost: true}, nil
	case "reset":
		return agent.Fault{Reset: n}, nil
	case "truncate":
		return agent.Fault{Truncate: n}, nil
	}
	status, err := strconv.Atoi(kind)
	if err != nil || status < 400 {
		return agent.Fault{}, fmt.Errorf("%q isn't a fault", f)
	}
	return agent.Fault{Status: status, RetryAfter: arg}, nil
}

// end::faults[]

// withFaults wraps a client's transport in the faults, if any.
func withFaults(hc *http.Client, faults []agent.Fault) *http.Client {
	if len(faults) == 0 {
		return hc
	}
	faulty := *hc
	faulty.Transport = &agent.Faulty{Next: hc.Transport, Faults: faults}
	return &faulty
}
