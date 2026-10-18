// Package task is the job each version of the agent does in chapter 16:
// diagnose one crash group with three tools, run_tests, issue_file,
// which a person must approve, and submit_diagnosis, the answer. The
// tools are plain Go functions here, so the versions differ only in the
// code around them.
package task

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Model is the one place the model ID is set for every version.
const Model = "anthropic/claude-haiku-5.5"

// Prompt is the system prompt every version sends.
const Prompt = "You triage one crash group in the remind service. " +
	"Run the tests of the package that crashed to confirm the bug, " +
	"file one issue that describes the cause (a person approves it " +
	"first), then call submit_diagnosis with your answer."

// Crash is the user message: the crash group to diagnose.
const Crash = "Crash group 7f3a, 4 reports: panic: assignment to entry " +
	"in nil map, at store.(*Store).Tag (store/tags.go:41), called from " +
	"api.(*Server).tagReminder. All four came after a reminder was " +
	"restored from the trash."

// TestArgs are run_tests' arguments. Each field carries two tags for
// its description: desc for the book's NewTool, jsonschema_description
// for the schema generators Eino and Genkit use.
type TestArgs struct {
	Package string `json:"package" jsonschema:"required" desc:"a package directory, such as ./store" jsonschema_description:"a package directory, such as ./store"`
}

// IssueArgs are issue_file's arguments.
type IssueArgs struct {
	Title string `json:"title" jsonschema:"required" desc:"one line naming the bug" jsonschema_description:"one line naming the bug"`
	Body  string `json:"body" jsonschema:"required" desc:"the cause, the evidence, and the reports" jsonschema_description:"the cause, the evidence, and the reports"`
}

// A Diagnosis is the agent's answer, submit_diagnosis' arguments.
type Diagnosis struct {
	Fingerprint string `json:"fingerprint" jsonschema:"required" desc:"the crash group's fingerprint" jsonschema_description:"the crash group's fingerprint"`
	File        string `json:"file" jsonschema:"required" desc:"the file where the code is wrong" jsonschema_description:"the file where the code is wrong"`
	Function    string `json:"function" jsonschema:"required" desc:"the function where the code is wrong" jsonschema_description:"the function where the code is wrong"`
	Issue       int    `json:"issue" jsonschema:"required" desc:"the issue you filed" jsonschema_description:"the issue you filed"`
}

// Descriptions of the three tools, shared by every version.
const (
	TestsDoc  = "Run one package's tests and return their output."
	IssueDoc  = "File an issue in the tracker. A person approves each issue before it is filed."
	SubmitDoc = "Finish the job: submit the diagnosis. Call it once, at the end."
)

// ErrNoPackage is run_tests' error for a package that doesn't exist.
var ErrNoPackage = errors.New("no such package")

// Env is the world the tools act on: canned test output, an issue
// tracker in memory, and the person who approves issues. It is safe for
// concurrent use.
type Env struct {
	// Approve is the person's decision on an issue; nil approves all.
	Approve func(IssueArgs) bool

	mu        sync.Mutex
	asked     int
	issues    []IssueArgs
	diagnosis *Diagnosis
}

// Report is what a run did to the world.
type Report struct {
	Asked     int        // approval decisions requested
	Issues    int        // issues filed
	Diagnosis *Diagnosis // nil if the run ended without one
}

// Report returns what the run did so far.
func (e *Env) Report() Report {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Report{Asked: e.asked, Issues: len(e.issues),
		Diagnosis: e.diagnosis}
}

var packages = []string{"./api", "./store"}

const storeOutput = `--- FAIL: TestTagRestored (0.00s)
panic: assignment to entry in nil map [recovered]
	store.(*Store).Tag(...)
		store/tags.go:41
FAIL	example.com/remind/store	0.012s`

// RunTests returns the canned output of one package's tests. A package
// that doesn't exist is an error that lists the real ones, so a model
// can correct its call.
func (e *Env) RunTests(ctx context.Context, a TestArgs) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("run tests: %w", err)
	}
	if !slices.Contains(packages, a.Package) {
		return "", fmt.Errorf("%w %q; packages with tests: %s",
			ErrNoPackage, a.Package, strings.Join(packages, ", "))
	}
	if a.Package == "./store" {
		return storeOutput, nil
	}
	return "ok  \texample.com/remind/api\t0.020s", nil
}

// Decide asks the person about one issue and counts the request.
func (e *Env) Decide(a IssueArgs) bool {
	e.mu.Lock()
	e.asked++
	approve := e.Approve
	e.mu.Unlock()
	return approve == nil || approve(a)
}

// FileIssue files an issue that a person has already approved, and
// returns its number.
func (e *Env) FileIssue(ctx context.Context, a IssueArgs) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("file issue: %w", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.issues = append(e.issues, a)
	return fmt.Sprintf("filed issue #%d", len(e.issues)), nil
}

// Submit records the answer.
func (e *Env) Submit(ctx context.Context, d Diagnosis) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if d.Issue < 1 || d.Issue > len(e.issues) {
		return "", fmt.Errorf("issue %d wasn't filed; file one first",
			d.Issue)
	}
	e.diagnosis = &d
	return "accepted", nil
}

// Denied is the tool result for an issue the person refused.
const Denied = "the person didn't approve this issue; don't file it again"
