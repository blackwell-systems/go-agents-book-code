package history

import (
	"cmp"
	"context"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

type logArgs struct {
	File     string `json:"file,omitzero" desc:"a file path, such as store/store.go; leave out for the whole tree"`
	Function string `json:"function,omitzero" desc:"a function in file, such as (*Store).List, to see each commit's change to it"`
	Limit    int    `json:"limit,omitzero" desc:"how many commits, newest first; default 10, at most 20"`
}

type blameArgs struct {
	File  string `json:"file" desc:"a file path, such as store/store.go"`
	Start int    `json:"start,omitzero" desc:"the first line; default 1"`
	End   int    `json:"end,omitzero" desc:"the last line; default 200 lines after start"`
}

// tag::tools[]

// Tools returns the history tools for a workspace: code_history and
// code_blame. Both only read.
func Tools(r *Repo) []agent.Tool {
	hist := agent.NewTool("code_history",
		"List the commits that changed a file of remind's source, "+
			"newest first, with hash, date, and subject. Give a "+
			"function as well to see how each commit changed it. Use "+
			"it to find which commit brought a bug in.",
		func(ctx context.Context, a logArgs) (string, error) {
			return r.Log(ctx, a.File, a.Function, cmp.Or(a.Limit, 10))
		})
	blame := agent.NewTool("code_blame",
		"Show which commit last changed each line of a file, as "+
			"committed: line number, hash, date, subject, and the "+
			"line. Shows at most 200 lines; ask for a range with "+
			"start and end.",
		func(ctx context.Context, a blameArgs) (string, error) {
			return r.Blame(ctx, a.File, a.Start, a.End)
		})
	hist.ReadOnly, blame.ReadOnly = true, true
	return []agent.Tool{hist, blame}
}

// end::tools[]
