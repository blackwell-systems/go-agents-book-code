package audit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"
)

var start = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// clock returns a clock that moves forward one minute each call.
func clock() func() time.Time {
	t := start
	return func() time.Time {
		t = t.Add(time.Minute)
		return t
	}
}

func targets(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Target)
	}
	return out
}

func TestRing(t *testing.T) {
	tests := []struct {
		size, records int
		want          []string
	}{
		{size: 3, records: 0, want: nil},
		{size: 3, records: 2, want: []string{"t2", "t1"}},
		{size: 3, records: 3, want: []string{"t3", "t2", "t1"}},
		{size: 3, records: 4, want: []string{"t4", "t3", "t2"}},
		{size: 3, records: 7, want: []string{"t7", "t6", "t5"}},
		{size: 1, records: 5, want: []string{"t5"}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d/%d", tt.size, tt.records), func(t *testing.T) {
			l := New(tt.size, clock())
			for i := range tt.records {
				l.Record("ann", "user.delete", fmt.Sprintf("t%d", i+1), "")
			}
			if got := targets(l.Query(Filter{})); !slices.Equal(got, tt.want) {
				t.Errorf("Query = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQuery(t *testing.T) {
	l := New(10, clock())
	l.Record("ann", "user.delete", "t1", "")      // 09:01
	l.Record("bob", "list.share", "t2", "")       // 09:02
	l.Record("ann", "list.share", "t3", "")       // 09:03
	l.Record("ann", "user.delete", "t4", "")      // 09:04
	l.Record("bob", "user.import", "t5", "3 new") // 09:05

	tests := []struct {
		name string
		f    Filter
		want []string
	}{
		{"all", Filter{}, []string{"t5", "t4", "t3", "t2", "t1"}},
		{"actor", Filter{Actor: "ann"}, []string{"t4", "t3", "t1"}},
		{"action", Filter{Action: "list.share"}, []string{"t3", "t2"}},
		{"actor and action", Filter{Actor: "ann", Action: "user.delete"}, []string{"t4", "t1"}},
		{"since", Filter{Since: start.Add(3 * time.Minute)}, []string{"t5", "t4", "t3"}},
		{"since and actor", Filter{Actor: "bob", Since: start.Add(3 * time.Minute)}, []string{"t5"}},
		{"limit", Filter{Limit: 2}, []string{"t5", "t4"}},
		{"limit after filter", Filter{Actor: "ann", Limit: 2}, []string{"t4", "t3"}},
		{"limit above count", Filter{Limit: 50}, []string{"t5", "t4", "t3", "t2", "t1"}},
		{"no match", Filter{Actor: "carol"}, nil},
		{"since after last", Filter{Since: start.Add(time.Hour)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := targets(l.Query(tt.f)); !slices.Equal(got, tt.want) {
				t.Errorf("Query(%+v) = %v, want %v", tt.f, got, tt.want)
			}
		})
	}
}

func TestEntryFields(t *testing.T) {
	l := New(4, clock())
	l.Record("ann", "user.import", "users", "3 new")
	got := l.Query(Filter{})
	want := Entry{
		Time:   start.Add(time.Minute),
		Actor:  "ann",
		Action: "user.import",
		Target: "users",
		Detail: "3 new",
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("Query = %+v, want [%+v]", got, want)
	}
}

func TestWriteJSON(t *testing.T) {
	l := New(2, clock())
	l.Record("ann", "user.delete", "usr_1", "")
	l.Record("ann", "user.delete", "usr_2", "")
	l.Record("bob", "list.share", "lst_0001", "with vic as viewer")

	var buf bytes.Buffer
	if err := l.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	want := `{"time":"2026-03-01T09:02:00Z","actor":"ann","action":"user.delete","target":"usr_2"}
{"time":"2026-03-01T09:03:00Z","actor":"bob","action":"list.share","target":"lst_0001","detail":"with vic as viewer"}
`
	if got := buf.String(); got != want {
		t.Errorf("WriteJSON wrote\n%s\nwant\n%s", got, want)
	}
}

func TestWriteJSONEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := New(3, clock()).WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("WriteJSON wrote %q for an empty log", buf.String())
	}
}

type failWriter struct{}

var errWrite = errors.New("disk full")

func (failWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestWriteJSONError(t *testing.T) {
	l := New(3, clock())
	l.Record("ann", "user.delete", "usr_1", "")
	if err := l.WriteJSON(failWriter{}); !errors.Is(err, errWrite) {
		t.Errorf("err = %v, want %v", err, errWrite)
	}
}

func TestNewPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New(0) did not panic")
		}
	}()
	New(0, clock())
}

func TestConcurrent(t *testing.T) {
	const workers, each, size = 8, 200, 64
	l := New(size, func() time.Time { return start })
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				l.Record(fmt.Sprintf("w%d", w), "list.share", fmt.Sprintf("t%d", i), "")
			}
		})
	}
	for range 2 {
		wg.Go(func() {
			for range each {
				l.Query(Filter{Action: "list.share", Limit: 10})
				if err := l.WriteJSON(io.Discard); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if got := len(l.Query(Filter{})); got != size {
		t.Errorf("kept %d entries, want %d", got, size)
	}
}
