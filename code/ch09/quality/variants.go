package main

import (
	_ "embed"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
)

// basePrompt is the prompt the check starts from: chapter 7's, with a
// line on the tools chapter 8 adds. Each version adds to it, and the
// triage agent's prompt is the version that measured best.
//
//go:embed prompt-v1.md
var basePrompt string

// tag::variants[]

// The sentences the check adds to the prompt, one change per version.
// Each answers a failure chapter 7's runs showed: crashes already filed
// fixed again, old-version crashes "fixed", and fixes at the crash site
// when the bad data came from somewhere else.
const (
	duplicates = "Before investigating a group, search the open " +
		"issues. If one already describes the same crash, give the " +
		"group the status duplicate with that issue's number, and " +
		"don't fix or file it again."
	oldVersions = "Compare each report's version with the code " +
		"in your workspace. If a crash can't happen in the current " +
		"code, give it the status already_fixed and don't change " +
		"the code, but still file an issue saying which version " +
		"crashed and what fixed it."
	crashSite = "The function that crashed is often not the one " +
		"with the bug. If bad data caused the crash, find where " +
		"that data was stored or created and fix the check there, " +
		"rather than guarding the place that crashed."
)

// variants build each version of the system prompt from the base one.
var variants = map[string]func(base string) string{
	"v1":           func(b string) string { return b },
	"duplicates":   func(b string) string { return b + "\n\n" + duplicates },
	"old-versions": func(b string) string { return b + "\n\n" + oldVersions },
	"crash-site":   func(b string) string { return b + "\n\n" + crashSite },
	"all": func(b string) string {
		return b + "\n\n" + duplicates + "\n\n" + oldVersions + "\n\n" + crashSite
	},
}

// end::variants[]

// rootCausesRight reports whether the grade found every group's root
// cause right: every group was found, and every "root cause" check
// passed. Groups that are duplicates have no root cause to find.
func rootCausesRight(checks []catalog.Check) bool {
	n := 0
	for _, c := range checks {
		switch {
		case c.Name == "found" && !c.Pass:
			return false
		case c.Name == "root cause" && !c.Pass:
			return false
		case c.Name == "root cause":
			n++
		}
	}
	return n > 0
}
