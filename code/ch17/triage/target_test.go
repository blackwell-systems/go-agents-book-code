package triage

import (
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"

	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// billing is a service of someone else's, for the tests.
var billing = Target{Name: "billing", Module: "example.com/billing"}

// asBilling returns r as if billing had crashed: the same stack, with
// billing's module path and directory in place of remind's.
func asBilling(r Report) Report {
	r.Stack = strings.NewReplacer("example.com/remind/",
		"example.com/billing/", "/remind/", "/billing/").Replace(r.Stack)
	return r
}

// TestTargetFingerprint checks that another service's crashes group as
// remind's do under its own target, and not under remind's, which
// would see none of the service's frames and group by panic kind alone.
func TestTargetFingerprint(t *testing.T) {
	reports, err := LoadReports(filepath.Join(catalogDir, "g3-mixed-batch",
		"reports.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reports {
		b := asBilling(r)
		if got, want := billing.Fingerprint(b), Fingerprint(r); got != want {
			t.Errorf("%s: billing's fingerprint %s, want remind's %s",
				r.ID, got, want)
		}
		if got, want := billing.ownFrames(b.Stack),
			Remind.ownFrames(r.Stack); got != want || got == "" {
			t.Errorf("%s: billing's frames\n%s\nwant\n%s", r.ID, got, want)
		}
		if Remind.Fingerprint(b) == Fingerprint(r) {
			t.Errorf("%s: remind's fingerprint read billing's frames", r.ID)
		}
	}
	if (Target{}).Fingerprint(reports[0]) != Fingerprint(reports[0]) {
		t.Error("the zero Target isn't remind")
	}
}

// TestTargetText checks that the prompts and every tool's description
// and schema name the target, and that remind's are unchanged.
func TestTargetText(t *testing.T) {
	if got := Remind.Text(Prompt); got != Prompt {
		t.Error("Remind changed the prompt")
	}
	if got := billing.Text(GroupPrompt); !strings.HasPrefix(got,
		"You triage one group of crash reports for billing, a Go web") {
		t.Errorf("group prompt: %.80q", got)
	}
	if got := billing.Text("remind's reminders"); got != "billing's reminders" {
		t.Errorf("Text = %q", got)
	}
	issues, err := tracker.Open(filepath.Join(t.TempDir(), "issues.json"),
		time.Now)
	if err != nil {
		t.Fatal(err)
	}
	remind := regexp.MustCompile(`\bremind\b`)
	for _, target := range []Target{Remind, billing} {
		e := &Env{Issues: issues, Target: target}
		tools := append(DiagnoseTools(e), ExplainPackage(nil, e, agent.Limits{}),
			CommentTool(e))
		named := 0
		for _, tl := range tools {
			if !jsontext.Value(tl.Def.Parameters).IsValid() {
				t.Errorf("%s: %s's schema isn't JSON", target.Name,
					tl.Def.Name)
			}
			text := tl.Def.Description + string(tl.Def.Parameters)
			if target.Name == "billing" && remind.MatchString(text) {
				t.Errorf("%s still names remind: %s", tl.Def.Name, text)
			}
			if strings.Contains(text, target.Name+"'s") {
				named++
			}
		}
		if named < 5 {
			t.Errorf("%s: only %d tools name the target", target.Name,
				named)
		}
	}
}

// TestLoadTarget checks a target file: the name defaults to the module
// path's last element, the protected paths gain AlwaysProtected, and a
// value that could break out of a prompt or a path is refused.
func TestLoadTarget(t *testing.T) {
	for _, tc := range []struct {
		name, file, wantErr string
		want                Target
	}{
		{"defaults", `{"module": "example.com/acme/billing",
			"protected": ["auth/", "internal/pay.go"]}`, "",
			Target{Name: "billing", Module: "example.com/acme/billing"}},
		{"named", `{"name": "bill", "module": "example.com/billing"}`, "",
			Target{Name: "bill", Module: "example.com/billing"}},
		{"no module", `{"name": "billing"}`, "module", Target{}},
		{"quote", `{"name": "bill\"ing", "module": "example.com/b"}`,
			"name", Target{}},
		{"spaces", `{"module": "example.com/b illing"}`, "module", Target{}},
		{"outside", `{"module": "example.com/b", "protected": ["../x"]}`,
			"protected", Target{}},
		{"absolute", `{"module": "example.com/b", "protected": ["/etc"]}`,
			"protected", Target{}},
		{"unclean", `{"module": "example.com/b", "protected": ["a//b"]}`,
			"protected", Target{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "target.json")
			if err := os.WriteFile(file, []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadTarget(file)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one about %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tc.want.Name || got.Module != tc.want.Module {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			for _, p := range AlwaysProtected {
				if !slices.Contains(got.Protected.Paths, p) {
					t.Errorf("%s isn't protected", p)
				}
			}
		})
	}
}
