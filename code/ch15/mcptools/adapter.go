package mcptools

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch15/mcp"
)

// tag::server-config[]

// A Server says how far to trust one MCP server's tools.
type Server struct {
	Name string // your name for it: 1 to 20 letters, digits, hyphens

	// Pins, if set, are the digests of the tools a person reviewed; any
	// other tool, or one that changed, stops Tools with ErrUnreviewed.
	// Nil checks nothing.
	Pins Pins

	// Trusted turns the server's hints into the tools' safety flags:
	// read-only tools may then run in parallel. Leave it false for a
	// server you didn't write and haven't reviewed.
	Trusted bool

	// KeyTTL is how long the server keeps idempotency keys, for its
	// tools that declare they honor them; zero means for good (chapter
	// 15).
	KeyTTL time.Duration

	// After, if set, runs after each call to a tool that may have
	// changed something, such as to reload a copy of the server's data.
	After func(ctx context.Context, tool string) error

	Log *log.Logger // gets the tools left out for their schemas, if set
}

// end::server-config[]

// tag::tools[]

// Tools lists a server's tools once and wraps each as one of the
// agent's: a name from names, which must already hold the agent's own
// tools; the description and schema as the model may see them; no
// strict mode, since the schema is the server's; and the safety flags
// only if the server is trusted.
func Tools(ctx context.Context, c *mcp.Client, s Server,
	names Names) ([]agent.Tool, error) {
	infos, err := c.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("server %s: %w", s.Name, err)
	}
	var tools []agent.Tool
	for _, info := range infos {
		t, why, err := wrap(c, s, names, info)
		if err != nil {
			return nil, fmt.Errorf("server %s: %w", s.Name, err)
		}
		if why != "" {
			s.logf("server %s: tool %s left out: %s", s.Name,
				info.Name, why)
			continue
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// end::tools[]

// tag::wrap[]

// wrap makes one server tool an agent tool, or says why it is refused.
func wrap(c *mcp.Client, s Server, names Names, info mcp.ToolInfo) (
	agent.Tool, string, error) {
	if why, err := s.Pins.check(info); why != "" || err != nil {
		return agent.Tool{}, "", errors.Join(err,
			fmt.Errorf("%w: %s: %s", ErrUnreviewed, info.Name, why))
	}
	schema, why := modelSchema(info.InputSchema)
	if why != "" {
		return agent.Tool{}, why, nil
	}
	name, err := names.Add(s.Name, info.Name)
	if err != nil {
		return agent.Tool{}, "", err
	}
	t := agent.Tool{Def: agent.ToolDef{Name: name, Parameters: schema,
		Description: Describe(s.Name, info)}, // Strict stays false
		Timeout: cmp.Or(c.Timeout, time.Minute)} // chapter 12
	if s.Trusted && info.Annotations != nil {
		t.ReadOnly = is(info.Annotations.ReadOnly)
		t.Idempotent = is(info.Annotations.Idempotent)
	}
	if s.Trusted && !t.ReadOnly && declaresKeyed(info) { // chapter 15
		t.Keyed, t.KeyTTL = true, s.KeyTTL
	}
	t.Run = func(ctx context.Context, args jsontext.Value) (
		string, error) {
		return s.call(ctx, c, info.Name, args, t.ReadOnly)
	}
	return t, "", nil
}

// end::wrap[]

// modelSchema is the schema the model sees, or why the tool is refused.
func modelSchema(schema jsontext.Value) (jsontext.Value, string) {
	clean, err := CleanSchema(schema)
	if err != nil {
		return nil, err.Error()
	}
	return clean, ""
}

// tag::call[]

// call calls the server's tool. A server that has gone is a tool that
// can't run at all, agent.ErrToolFatal, which stops the run: every
// later call would fail the same way, and the model would keep trying.
// A result with isError set is a tool error, for the model to read.
func (s Server) call(ctx context.Context, c *mcp.Client, tool string,
	args jsontext.Value, readOnly bool) (string, error) {
	r, err := c.CallTool(withKey(ctx), tool, args)
	if errors.Is(err, mcp.ErrServerGone) {
		return "", fmt.Errorf("%w: MCP server %s: %w",
			agent.ErrToolFatal, s.Name, err)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	if s.After != nil && !readOnly {
		if err := s.After(ctx, tool); err != nil {
			return "", fmt.Errorf("%w: after %s: %w",
				agent.ErrToolFatal, tool, err)
		}
	}
	text := clip(visible(r.Text()), maxResult) // see chapters 8 and 14
	if r.IsError {
		return "", errors.New(text)
	}
	return text, nil
}

// maxResult is the most of one result the model sees. The agent's own
// tools cap their output; a server's can return anything.
const maxResult = 16 << 10

// end::call[]

func (s Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Printf(format, args...)
	}
}

func is(b *bool) bool { return b != nil && *b }

// tag::offer[]

// Offer turns the agent's own tools into tools an MCP server offers,
// with the same names, descriptions, schemas, and safety flags.
func Offer(tools []agent.Tool) []mcp.Tool {
	var out []mcp.Tool
	for _, t := range tools {
		out = append(out, mcp.Tool{Name: t.Def.Name,
			Description: t.Def.Description,
			InputSchema: t.Def.Parameters, ReadOnly: t.ReadOnly,
			Idempotent: t.Idempotent, Call: keyed(t.Run),
			Meta: keyedMeta(t)})
	}
	return out
}

// end::offer[]
