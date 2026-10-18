package triage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch11/agent"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// tag::issue-read[]

type issueReadArgs struct {
	ID int `json:"id" desc:"the issue number, such as 101"`
}

// issueRead shows one issue in full. Chapter 7's smoke test found the
// gap: asked to comment on issue 101, models reached for a tool that
// would look the issue up first, and issue_search only lists.
func issueRead(e *Env) agent.Tool {
	return agent.NewTool("issue_read",
		"Read one issue in full by its number: title, status, "+
			"fingerprint, body, and comments. Use issue_search to find "+
			"issues; use this to read one before commenting on it or "+
			"marking a crash as its duplicate.",
		func(_ context.Context, a issueReadArgs) (string, error) {
			is, err := e.Issues.Get(a.ID)
			if errors.Is(err, tracker.ErrNotFound) {
				return "", fmt.Errorf("no issue #%d; issue_search lists "+
					"the open issues", a.ID)
			}
			if err != nil {
				return "", fmt.Errorf("read issue #%d: %w", a.ID, err)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "#%d %s\nstatus: %s\n", is.ID, is.Title, is.Status)
			if is.Fingerprint != "" {
				fmt.Fprintf(&b, "fingerprint: %s\n", is.Fingerprint)
			}
			fmt.Fprintf(&b, "\n%s\n", limitText(is.Body, 4<<10))
			for i, c := range is.Comments {
				fmt.Fprintf(&b, "\ncomment %d, %s:\n%s\n", i+1,
					c.Created.Format("2006-01-02 15:04"), limitText(c.Body, 2<<10))
			}
			return b.String(), nil
		})
}

// end::issue-read[]
