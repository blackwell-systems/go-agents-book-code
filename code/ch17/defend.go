package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/safety"
	"github.com/blackwell-systems/go-agents-book/code/ch17/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// tag::defense[]

// A defense is chapter 14's defenses for one run: middleware for the
// tools, inner and outer, the approver the run asks, the marker's rule
// for the instruction, and what the after-run check needs.
type defense struct {
	inner    []agent.ToolWare // inside chapter 12's wrappers
	outer    []agent.ToolWare // outside them: egress, then the gate
	approver agent.Approver
	rule     string
	box      safety.Sandbox // the tests', for the output and records
	gate     *safety.Gate   // for the audit log
	cleanup  []func() error

	protected *safety.Protected // for the check after the run
}

// end::defense[]

// A defenseConfig says which defenses a run gets: level is none (as
// chapter 13 left the agent), marked (the data marker alone), or all;
// approve says who decides (see approverFor); minRung is the lowest
// sandbox rung the run accepts, 3 unless asked for less.
type defenseConfig struct {
	level, approve string
	minRung        int
}

// tag::new-defense[]

// newDefense builds the defenses c names for job j, with marker m.
func newDefense(ctx context.Context, getenv func(string) string,
	c defenseConfig, j *taskrun.Job, m *safety.Marker,
	out io.Writer) (*defense, error) {
	d := &defense{}
	switch c.level {
	case "", "none":
		return d, nil
	case "marked":
		d.rule = "\n\n" + m.Rule()
		d.inner = triage.Defend(j.Env.Dir, safety.Policy{Off: true}, m)
		return d, nil
	case "all":
	default:
		return nil, fmt.Errorf("no defenses %q; try none, marked, or all",
			c.level)
	}
	d.rule = "\n\n" + m.Rule()
	if err := d.sandbox(ctx, getenv, j, c.minRung, out); err != nil { // <1>
		return nil, err
	}
	p := safety.DefaultPolicy(*d.protected)
	var err error
	if d.approver, p.Off, err = d.approverFor(c.approve, out); err != nil {
		return nil, err
	}
	eg := triage.NewEgress(append(secretValues(getenv),
		m.Boundary())...) // every secret the program holds (chapter 17)
	d.gate = &safety.Gate{Dir: j.Env.Dir, Evidence: triage.Evidence(j.Env)}
	d.inner = append(triage.Defend(j.Env.Dir, p, m),
		d.protected.Watch(j.Env.Dir, "run_tests", "code_patch",
			"test_write")) // <2>
	d.outer = []agent.ToolWare{eg.Outgoing(triage.Outgoing...),
		d.gate.Wrap} // <3>
	return d, nil
}

// end::new-defense[]

// approverFor returns the approver approve names: ask, at the
// terminal; deny, every call that needs a decision; files, a script
// for evals that denies changes to files and allows the rest; later,
// nobody yet, so the run pauses; or off, approval turned off.
func (d *defense) approverFor(approve string, out io.Writer) (
	agent.Approver, bool, error) {
	switch approve {
	case "ask":
		a := &safety.LineApprover{In: os.Stdin, Out: out}
		d.cleanup = append(d.cleanup, a.Close)
		return a, false, nil
	case "deny":
		return agent.ApproverFunc(denyAll), false, nil
	case "files":
		return agent.ApproverFunc(denyFiles), false, nil
	case "later":
		return nil, false, nil
	case "off":
		return nil, true, nil
	}
	return nil, false, fmt.Errorf("no approver %q; try ask, deny, files, "+
		"later, or off", approve)
}

// tag::sandbox-setup[]

// sandbox runs the job's go test commands in the sandbox, at minRung
// or above, with the protected paths and the existing tests fixed.
func (d *defense) sandbox(ctx context.Context,
	getenv func(string) string, j *taskrun.Job, minRung int,
	out io.Writer) error {
	prot := j.Env.Target.Protected // remind's, or -target's (chapter 17)
	prot.Original = j.Env.Original // the agent may edit its own tests
	d.protected = &prot
	fixed, err := prot.Fixed(j.Env.Original)
	if err != nil {
		return err //nolint:wrapcheck // Fixed says what it was doing
	}
	goroot, err := goRoot(ctx)
	if err != nil {
		return err
	}
	s := safety.NewSandbox("", goroot)
	if s.Rung < minRung {
		return fmt.Errorf("sandbox: rung %d only; -sandbox 2 "+
			"accepts that", s.Rung)
	}
	s.Scratch, err = os.MkdirTemp("", "triage-scratch-")
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	rm := func() error { return os.RemoveAll(s.Scratch) }
	d.cleanup = append(d.cleanup, rm)
	s.Fixed = fixed // <1>
	d.box, j.Env.Tests = s, s.Test
	return d.checkKeys(getenv, j.Env.Dir, out) // <2>
}

// end::sandbox-setup[]

// goRoot returns the Go toolchain's root, which test code must read.
func goRoot(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "go", "env", "GOROOT").Output()
	if err != nil {
		return "", fmt.Errorf("find the Go toolchain: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// close releases what the defenses hold: the terminal's reader and the
// sandbox's scratch directory.
func (d *defense) close() error {
	var errs []error
	for _, f := range d.cleanup {
		errs = append(errs, f())
	}
	return errors.Join(errs...)
}

// tag::scripted[]

// denyAll denies every call that needs a decision. A question about
// an unknown outcome is left for a person who can check (chapter 15).
func denyAll(_ context.Context, r agent.ApprovalRequest) (
	agent.ApprovalDecision, error) {
	if r.Kind == agent.KindUnknownOutcome {
		return agent.ApprovalDecision{}, agent.ErrNoDecision
	}
	return agent.ApprovalDecision{Hash: r.Hash, By: "script",
		Reason: "a person said no: " + r.Why}, nil
}

// denyFiles is the scripted approver the evals use: it denies a call
// that changes a protected file, which no injection task's fix needs,
// and allows filing and commenting, whose text the egress check has
// already read. It stands in for a person, and is kinder than one: it
// approves any issue text the check lets through.
func denyFiles(ctx context.Context, r agent.ApprovalRequest) (
	agent.ApprovalDecision, error) {
	if r.Kind == agent.KindUnknownOutcome { // only a person can check
		return agent.ApprovalDecision{}, agent.ErrNoDecision
	}
	if slices.ContainsFunc(r.Targets, func(t string) bool {
		return !strings.Contains(t, ":") // a file, not issue:...
	}) {
		return denyAll(ctx, r)
	}
	return agent.ApprovalDecision{Approved: true, Hash: r.Hash,
		By: "script"}, nil
}

// end::scripted[]

// openAudit starts the gate's audit log in file, if one is named,
// keyed with $AUDIT_KEY, or the key in the file $AUDIT_KEY_FILE names,
// which takeSecrets has already read and removed from the environment.
func (d *defense) openAudit(getenv func(string) string, file string) error {
	if file == "" || d.gate == nil {
		return nil
	}
	key := getenv("AUDIT_KEY")
	if key == "" {
		return errors.New("-audit needs a key in $AUDIT_KEY or " +
			"$AUDIT_KEY_FILE")
	}
	a, f, err := safety.OpenAudit(file, []byte(key))
	if err != nil {
		return fmt.Errorf("open the audit log: %w", err)
	}
	d.gate.Audit = a
	d.cleanup = append(d.cleanup, f.Close)
	return nil
}

// printPending lists the calls a paused run left waiting.
func printPending(p *printer, calls []agent.ToolCall) {
	if len(calls) == 0 {
		return
	}
	p.printf("waiting for a person: %d calls\n", len(calls))
	for _, c := range calls {
		shown, _ := safety.Display(string(c.Arguments))
		p.printf("  %s %s\n", safety.DisplayPath(c.Name), shown)
	}
}

// tag::review[]

// review compares the workspace with the original after the run and
// lists every change to a protected part of it, however it was made,
// for a person to look at before the change goes anywhere: test code
// can write files that no tool call named.
func (d *defense) review(p *printer, j *taskrun.Job) error {
	if d.protected == nil {
		return nil
	}
	vs, err := d.protected.Diff(j.Env.Original, j.Env.Dir)
	if err != nil {
		return fmt.Errorf("check protected paths: %w", err)
	}
	for _, v := range vs {
		p.printf("protected: %s: %s; a person should look\n",
			safety.DisplayPath(v.Path), v.Why)
	}
	return nil
}

// end::review[]

// rungName says what a sandbox rung is on this system.
func rungName(rung int) string {
	if rung < 3 {
		return "rung 2, the hardened runner alone"
	}
	if runtime.GOOS == "darwin" {
		return "rung 3, in Seatbelt"
	}
	return "rung 3, in bubblewrap"
}

// recall rebuilds the gate's denials from the audit log when a saved
// run is resumed, so what a person refused before a restart stays
// refused after it (chapter 15).
func (d *defense) recall(getenv func(string) string, file string,
	r *savedRun) error {
	if file == "" || d.gate == nil || !r.resumed {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("recall denials: %w", err)
	}
	defer f.Close() // read-only
	if _, err := d.gate.Recall(f, []byte(getenv("AUDIT_KEY")),
		r.st.RunID); err != nil {
		return fmt.Errorf("recall denials: %w", err)
	}
	return nil
}

// tag::check-keys[]

// checkKeys refuses a key file in the workspace, or one test code could
// read at rung 3, and on macOS warns about a key in the environment,
// which test code can read there whatever the profile says.
func (d *defense) checkKeys(getenv func(string) string, dir string,
	out io.Writer) error {
	for _, k := range secretNames {
		file := getenv(k + "_FILE")
		if file == "" && getenv(k) != "" && d.box.Rung >= 3 &&
			runtime.GOOS == "darwin" {
			_, err := fmt.Fprintf(out, "warning: %s is in the "+
				"environment, which test code can read; set %s_FILE "+
				"instead\n", k, k)
			if err != nil {
				return fmt.Errorf("write output: %w", err)
			}
		}
		if file == "" {
			continue
		}
		readable, err := d.box.Reads(dir, file)
		if err != nil {
			return fmt.Errorf("check %s_FILE: %w", k, err)
		}
		if safety.Inside(file, dir) || d.box.Rung >= 3 && readable {
			return fmt.Errorf("%s_FILE %s: %w", k, file, errKeyReadable)
		}
	}
	return nil
}

// end::check-keys[]
