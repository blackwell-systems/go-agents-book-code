package evals

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func rec(model string, cost float64, verdict string) Record {
	r := Record{}
	r.Model, r.Cost, r.Verdict = model, cost, verdict
	return r
}

func TestSpending(t *testing.T) {
	rs := []Record{rec("a", 0.01, "pass"), rec("a", 0.03, "unscored"),
		rec("b", 0.10, "fail")}
	spent := Spending(rs)
	if len(spent) != 2 || spent[0].Runs != 2 || spent[0].Unscored != 1 ||
		math.Abs(spent[0].Total-0.04) > 1e-12 || spent[0].Most != 0.03 {
		t.Fatalf("Spending = %+v", spent)
	}
	if spent[0].PerScored != 0.04 { // $0.04 over the one scored run
		t.Errorf("PerScored = %v, want 0.04", spent[0].PerScored)
	}
	mean, p90, per := Project(rs, map[string]int{"a": 10, "b": 1}, 1)
	if math.Abs(per["a"][0]-0.44) > 1e-12 ||
		math.Abs(per["b"][0]-0.20) > 1e-12 || math.Abs(mean-0.64) > 1e-12 ||
		p90 < mean {
		t.Errorf("Project = %v, %v, %v", mean, p90, per)
	}
	if mean, _, _ := Project(rs, map[string]int{"c": 5}, 1); !math.IsNaN(mean) {
		t.Errorf("a model with no pilot projects at %v, want NaN", mean)
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"ok.json":     `{"cost":0.0402,"id":"gen-14025"}`,
		"credit.json": `{"error":{"code":402,"message":"Insufficient credits"}}`,
		"cache.txt":   "open /Users/x/Library/Caches/go-build/ab: no such file\n",
		"tool.txt":    "go: go.mod requires go >= 1.27 (running go 1.26.4)\n",
		"tls.txt":     "local error: tls: bad record MAC\nHTTP 402\n",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text),
			0o644); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, h := range hits {
		got[filepath.Base(h.File)+" "+h.Fault] = "x"
	}
	for _, want := range []string{"credit.json 402",
		"cache.txt Library/Caches/go-build", "tool.txt go >= 1.27",
		"tls.txt bad record MAC", "tls.txt 402"} {
		if got[want] == "" {
			t.Errorf("Scan missed %s; found %v", want, hits)
		}
	}
	if len(hits) != 5 {
		t.Errorf("Scan found %d hits, want 5 (none in ok.json): %v",
			len(hits), hits)
	}
}
