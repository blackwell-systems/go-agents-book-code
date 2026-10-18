package evals

import "fmt"

// tag::tier-type[]

// A Tier says what a task's failures mean.
type Tier string

const (
	// Floor tasks are the regression floor: ones a working agent
	// should pass nearly every time. A drop against their own history
	// (Drops) is a bug to look at, in the agent or the task.
	Floor Tier = "floor"
	// Hard tasks measure capability: ones where the obvious fix is
	// wrong or the evidence is outside the code; their pass rate is
	// what a change tries to move.
	Hard Tier = "hard"
)

// end::tier-type[]

// tag::tiers[]

// tiers assigns each of the catalog's single tasks, by category, with
// the misleading tasks split by how often the prototype sweep passed
// them, so the sweep can't also confirm the split; chapter 13's runs
// confirm or move a task. The batch, every report at once, is the
// third tier and has no entry here.
var tiers = byTier(map[Tier][]string{
	Floor: {"d1-empty-snooze", "d2-clock-no-minutes", "d3-page-offset",
		"d4-preview-multibyte", "d5-bearer-no-token",
		"d6-dispatcher-stop-twice", "i1-direct", "i2-encoded",
		"i3-split-fields", "i4-in-stack", "i5-in-logs",
		"i6-exfil-header", "i7-go-mod", "i8-exfil-link",
		"dl1-lock-order", "r1-status-race", "h1-paging-commit",
		"long-weekly-zone", "m1-done-after-delete", "m3-zero-interval",
		"m4-imported-user-prefs", "m5-admin-user-copy"},
	Hard: {"m2-unknown-zone-list", "m6-bad-zone-digest",
		"m7-priority-range", "m8-quiet-hours", "m9-closed-account-list",
		"f1-old-missing-unit", "f2-old-digest-eighth-day",
		"g1-send-two-causes", "g2-missing-auth", "g3-mixed-batch"},
})

// end::tiers[]

// tag::tuning[]

// tuning lists the tasks a prompt or tool change may be tried against:
// chapter 8's five and seven more, across both tiers. Every other task
// is for confirmation only, so a change is never judged on the tasks
// it was tuned to.
var tuning = set("m2-unknown-zone-list", "m1-done-after-delete",
	"g3-mixed-batch", "f2-old-digest-eighth-day", "d1-empty-snooze",
	"i1-direct", "i4-in-stack", "m3-zero-interval",
	"g1-send-two-causes", "dl1-lock-order", "d3-page-offset",
	"m7-priority-range")

// end::tuning[]

// TierOf returns a task's tier, and whether the task has one.
func TierOf(task string) (Tier, bool) {
	t, ok := tiers[task]
	return t, ok
}

// Tuning reports whether a task is in the tuning set.
func Tuning(task string) bool { return tuning[task] }

// byTier inverts a list of tasks per tier. A task listed twice is a
// mistake in the list, and would otherwise land in whichever tier the
// map visits last.
func byTier(m map[Tier][]string) map[string]Tier {
	out := map[string]Tier{}
	for tier, tasks := range m {
		for _, t := range tasks {
			if prev, ok := out[t]; ok {
				panic(fmt.Sprintf("evals: %s in tiers %s and %s", t, prev, tier))
			}
			out[t] = tier
		}
	}
	return out
}

func set(xs ...string) map[string]bool {
	out := map[string]bool{}
	for _, x := range xs {
		out[x] = true
	}
	return out
}
