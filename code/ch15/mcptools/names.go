// Package mcptools turns the tools of MCP servers into the agent's
// tools, with the rules for trusting someone else's tools: names
// prefixed with the server's, descriptions and schemas stripped, labeled
// and capped, hints treated as claims, and definitions pinned to the
// ones a person reviewed (chapter 10).
package mcptools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// tag::names[]

// maxName is the longest tool name the model APIs all accept: OpenAI's
// takes up to 64 characters, Anthropic's up to 128, both of letters,
// digits, underscores, and hyphens. MCP allows dots too, and up to 128.
const maxName = 64

// serverName is what a server's name may be: letters, digits, and
// hyphens. With no underscore in it, the first "__" in a tool's name
// always ends the server's part, so two servers can't produce the same
// name from different tools. The name is yours, from your
// configuration, never the one the server reports about itself.
var serverName = regexp.MustCompile(`^[A-Za-z0-9-]{1,20}$`)

// ToolName is the name the model sees for a server's tool: the server's
// name, two underscores, and the tool's, with any character the APIs
// don't accept replaced by an underscore. A name too long is shortened,
// with a hash of the whole name at the end so two long names stay
// apart.
func ToolName(server, tool string) string {
	name := server + "__" + clean(tool)
	if len(name) <= maxName {
		return name
	}
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	return name[:maxName-9] + "_" + hex.EncodeToString(sum[:4])
}

// end::names[]

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}

// tag::registry[]

// Names holds every tool name the model is offered, from every server
// and from the agent's own tools, with where each came from. Two tools
// with one name would leave the model unable to choose and the harness
// unable to route the call, so the second is refused.
type Names map[string]string

// Reserve records one of the agent's own tools.
func (n Names) Reserve(name string) error {
	return n.add(name, "the agent's own "+name)
}

// Add records a server's tool and returns the name the model sees.
func (n Names) Add(server, tool string) (string, error) {
	if !serverName.MatchString(server) {
		return "", fmt.Errorf("server name %q: use 1 to 20 letters, "+
			"digits, and hyphens", server)
	}
	name := ToolName(server, tool)
	return name, n.add(name, fmt.Sprintf("%s's %q", server, tool))
}

func (n Names) add(name, from string) error {
	if other, ok := n[name]; ok {
		return fmt.Errorf("%s and %s both become %s", other, from, name)
	}
	n[name] = from
	return nil
}

// end::registry[]
