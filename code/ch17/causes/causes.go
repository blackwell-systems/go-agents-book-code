// Package causes keeps the record that stops two fingerprint groups
// from filing two issues for one cause (chapter 17). Chapter 9 joined the
// groups' diagnoses after they finished and then filed; here each
// group files for itself, so a cause whose reports landed under two
// fingerprints would be filed twice. The record is checked, across
// every group, before an issue_file call goes out.
package causes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// tag::entry[]

// An Entry is one cause and the run that took it: reserved before the
// run's issue_file call goes out, with the call's idempotency key, and
// given the issue's number when the call returns.
type Entry struct {
	Cause       string    `json:"cause"` // file:function, normalized
	Fingerprint string    `json:"fingerprint,omitzero"`
	Run         string    `json:"run"`
	Key         string    `json:"key,omitzero"`
	Issue       int       `json:"issue,omitzero"`
	At          time.Time `json:"at"`
}

// end::entry[]

// filing names the tools a guard watches: issue_file, and its name
// through chapter 10's MCP tracker.
var filing = []string{"issue_file", "tracker__issue_file"}

// A Record holds the entries in a directory, one file per cause, so a
// restarted service still knows which causes are filed.
type Record struct {
	dir string
	mu  sync.Mutex

	// Active reports whether a run is still going: a reservation by a
	// run that stopped without filing is taken over.
	Active func(run string) bool

	// Open reports whether an issue is still open: a cause whose issue
	// was closed can be filed again, as a crash that came back.
	Open func(ctx context.Context, issue int) (bool, error)

	// ByKey finds the issue a call filed with this idempotency key, if
	// the tracker has it: a run that stopped after the tracker took its
	// call, before the record heard back, did file.
	ByKey func(key string) (int, bool)

	Log *slog.Logger // what the record couldn't save; default slog's
}

// Open returns the record kept in dir.
func Open(dir string) (*Record, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("causes: %w", err)
	}
	return &Record{dir: dir}, nil
}

// tag::guard[]

// Guard returns middleware for issue_file in run, whose workspace is
// ws: before the call goes out, it reserves the call's root cause. If
// another group already has an open issue for it, the call is refused
// with an error that says to comment instead; if another group is
// filing one now, the call waits, and the run resumes when that
// filing settles.
func (r *Record) Guard(run, ws string) agent.ToolWare {
	return func(t agent.Tool) agent.Tool {
		if !slices.Contains(filing, t.Def.Name) {
			return t
		}
		call := t.Run
		t.Run = func(ctx context.Context, args jsontext.Value) (string,
			error) {
			c, err := causeOf(args, ws)
			if err != nil {
				return "", err
			}
			if c.cause != "" {
				if err := r.reserve(ctx, run, c); err != nil {
					return "", err
				}
			}
			out, err := call(ctx, args)
			if c.cause != "" {
				r.settle(run, c.cause, out, err)
			}
			return out, err
		}
		return t
	}
}

// end::guard[]

// A claim is what a call files for: its cause and its group.
type claim struct {
	cause, fingerprint string
}

// tag::reserve[]

// reserve takes the cause for run, or says why it can't.
func (r *Record) reserve(ctx context.Context, run string,
	c claim) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok, err := r.read(c.cause)
	if err != nil {
		return err
	}
	if ok && e.Run != run && e.Issue == 0 && !r.active(e.Run) {
		e.Issue, _ = r.byKey(e.Key) // <1>
	}
	switch {
	case ok && e.Run == run:
		return nil // this run's own retry or second call
	case ok && e.Issue > 0:
		open, err := r.open(ctx, e.Issue)
		if err != nil {
			return fmt.Errorf("check #%d: %w", e.Issue, err)
		}
		if open {
			return covered(e, c)
		}
	case ok && r.active(e.Run):
		return waiting(ctx, c) // <2>
	}
	key, _ := agent.KeyFrom(ctx)
	return r.write(Entry{Cause: c.cause, Fingerprint: c.fingerprint,
		Run: run, Key: key, At: time.Now().UTC()})
}

// end::reserve[]

// covered is the refusal for a cause whose issue is open.
func covered(e Entry, c claim) error {
	whose := "another fingerprint group"
	if e.Fingerprint != "" && e.Fingerprint == c.fingerprint {
		whose = "this group, by an earlier run"
	}
	//nolint:wrapcheck // the message the model reads
	return agent.ArgsError(fmt.Sprintf("not filed: #%d, filed for %s, "+
		"already covers %s; add this group's reports to it with "+
		"issue_comment", e.Issue, whose, c.cause))
}

// tag::waiting[]

// waiting pauses the call while another group's filing of the same
// cause waits for a person: if the person says no, this group files;
// if yes, it comments on that issue. The request asks nobody; the
// harness resumes the run when Busy says the cause is settled.
func waiting(ctx context.Context, c claim) error {
	call, _ := agent.CallFrom(ctx)
	return &agent.ApprovalPending{Requests: []agent.ApprovalRequest{{
		Kind: agent.KindWait, Run: agent.NameFrom(ctx), Call: call,
		Why: "waiting: another group is filing an issue for " +
			c.cause,
		Targets: []string{"cause:" + c.cause},
		Hash:    "wait " + c.cause}}}
}

// end::waiting[]

// Busy reports whether a cause is reserved by a run whose filing
// hasn't settled: the target of a KindWait request, "cause:...".
func (r *Record) Busy(target string) bool {
	cause, ok := strings.CutPrefix(target, "cause:")
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, found, err := r.read(cause)
	return err == nil && found && e.Issue == 0 && r.active(e.Run)
}

var filedRE = regexp.MustCompile(`^filed #(\d+)`)

// settle records the issue a call filed, or drops the reservation of
// a call that filed nothing. A call waiting for a person, or one whose
// outcome is unknown, keeps it: it may file yet.
func (r *Record) settle(run, cause, out string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok, rerr := r.read(cause)
	if rerr != nil || !ok || e.Run != run || e.Issue > 0 {
		return
	}
	if m := filedRE.FindStringSubmatch(out); err == nil && m != nil {
		e.Issue, _ = strconv.Atoi(m[1])
		if werr := r.write(e); werr != nil { // found again by its key
			r.log().Warn("save a filed cause", "cause", cause,
				"err", werr)
		}
		return
	}
	_, paused := errors.AsType[*agent.ApprovalPending](err)
	if paused || errors.Is(err, agent.ErrOutcomeUnknown) {
		return // the call may still file: keep the cause
	}
	if rerr := os.Remove(r.file(cause)); rerr != nil {
		r.log().Warn("free a cause", "cause", cause, "err", rerr)
	}
}

func (r *Record) active(run string) bool {
	return r.Active == nil || r.Active(run)
}

func (r *Record) open(ctx context.Context, issue int) (bool, error) {
	if r.Open == nil {
		return true, nil
	}
	return r.Open(ctx, issue)
}

func (r *Record) byKey(key string) (int, bool) {
	if r.ByKey == nil || key == "" {
		return 0, false
	}
	return r.ByKey(key)
}

func (r *Record) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}

// Lookup returns the entry for a cause, if there is one.
func (r *Record) Lookup(file, function string) (Entry, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read(Normalize(file, function))
}

// tag::normalize[]

// Normalize names a cause the same way however the model wrote it. The
// file is the path from the module's root: "./store/store.go",
// "remind/store/store.go", and "example.com/remind/store/store.go" are
// one file. The function keeps its receiver, so (*Store).Get and
// (*Cache).Get differ, but loses the pointer, the package, and the
// numbers of closures: "(*Store).Get", "Store.Get", "store.Store.Get",
// and "Store.Get.func1" are one function.
func Normalize(file, function string) string {
	file = cleanFile(file)
	fn := strings.TrimSpace(function)
	fn = fn[strings.LastIndexByte(fn, '/')+1:]
	fn = strings.NewReplacer("(", "", ")", "", "*", "").Replace(fn)
	var parts []string
	for p := range strings.SplitSeq(fn, ".") {
		if p != "" && !closure.MatchString(p) {
			parts = append(parts, p)
		}
	}
	if len(parts) > 1 && parts[0] == path.Base(path.Dir(file)) {
		parts = parts[1:] // the package's name
	}
	return strings.ToLower(file) + ":" + strings.Join(parts, ".")
}

// end::normalize[]

// cleanFile is a file's path from the module's root, its case kept.
func cleanFile(file string) string {
	file = strings.TrimPrefix(strings.TrimSpace(file), "./")
	for _, p := range []string{"example.com/remind/", "remind/"} {
		file = strings.TrimPrefix(file, p)
	}
	return path.Clean(file)
}

var closure = regexp.MustCompile(`^(func)?\d+$`)

// causeOf reads root_cause and the fingerprint from issue_file's
// arguments; an empty cause means the call names none, as for a group
// whose cause wasn't found. A file that isn't in the workspace is
// refused: a cause recorded under a wrong path guards nothing.
func causeOf(args jsontext.Value, ws string) (claim, error) {
	var a struct {
		Fingerprint string `json:"fingerprint"`
		RootCause   struct {
			File     string `json:"file"`
			Function string `json:"function"`
		} `json:"root_cause"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		// The tool itself reports bad arguments.
		return claim{}, nil //nolint:nilerr // see above
	}
	if a.RootCause.File == "" || a.RootCause.Function == "" {
		return claim{}, nil
	}
	c := claim{cause: Normalize(a.RootCause.File, a.RootCause.Function),
		fingerprint: a.Fingerprint}
	if ws != "" {
		file := cleanFile(a.RootCause.File) // as written: case matters
		_, err := os.Stat(filepath.Join(ws, filepath.FromSlash(file)))
		if err != nil || !filepath.IsLocal(file) {
			//nolint:wrapcheck // the message the model reads
			return claim{}, agent.ArgsError(fmt.Sprintf(
				"root_cause.file %q isn't in the workspace; give its "+
					"path from the module root, such as store/store.go",
				a.RootCause.File))
		}
	}
	return c, nil
}

func (r *Record) file(cause string) string {
	h := sha256.Sum256([]byte(cause))
	return filepath.Join(r.dir, hex.EncodeToString(h[:8])+".json")
}

func (r *Record) read(cause string) (Entry, bool, error) {
	b, err := os.ReadFile(r.file(cause))
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("causes: %w", err)
	}
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return Entry{}, false, fmt.Errorf("causes: %w", err)
	}
	return e, true, nil
}

// write saves an entry: a temporary file, synced, then renamed.
func (r *Record) write(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("causes: %w", err)
	}
	f, err := os.CreateTemp(r.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("causes: %w", err)
	}
	defer os.Remove(f.Name()) // fails once renamed, as intended
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), r.file(e.Cause))
	}
	if err != nil {
		return fmt.Errorf("causes: %w", err)
	}
	return nil
}
