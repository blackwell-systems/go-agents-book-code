package notify

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"example.com/remind/store"
)

var t0 = time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)

func TestPreview(t *testing.T) {
	short := "call the dentist"
	if got := Preview(short); got != short {
		t.Errorf("Preview(%q) = %q", short, got)
	}
	long := strings.Repeat("a", 100)
	if got := Preview(long); got != strings.Repeat("a", previewLen)+"…" {
		t.Errorf("Preview(100 a's) = %q", got)
	}
}

func TestBuildDigest(t *testing.T) {
	occ := []store.Occurrence{
		{Text: "a", At: t0.Add(time.Hour)},
		{Text: "b", At: t0.Add(25 * time.Hour)},
		{Text: "c", At: t0.Add(26 * time.Hour)},
	}
	d := BuildDigest(t0, 3, time.UTC, occ)
	if len(d.Days) != 3 || d.Days[0].Date != "2026-09-07" {
		t.Fatalf("days = %+v", d.Days)
	}
	if n := []int{len(d.Days[0].Items), len(d.Days[1].Items), len(d.Days[2].Items)}; n[0] != 1 || n[1] != 2 || n[2] != 0 {
		t.Errorf("items per day = %v, want [1 2 0]", n)
	}
}

func TestValidChannel(t *testing.T) {
	if !ValidChannel("email") || ValidChannel("pigeon") {
		t.Error("ValidChannel gives the wrong answer")
	}
}

func TestFlush(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	s := store.New(func() time.Time { return t0 })
	if err := s.AddUser(store.User{ID: "u1", Email: "ana@example.com"}); err != nil {
		t.Fatal(err)
	}
	r := s.Create("u1", "stand up", t0.Add(-time.Minute), nil)
	d := NewDispatcher(s, log, func() time.Time { return t0 })
	d.flush(context.Background())
	if !strings.Contains(logs.String(), "ana@example.com") {
		t.Errorf("logs = %s, want an email to ana", logs.String())
	}
	got, err := s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Done {
		t.Error("reminder not marked done after sending")
	}
}

func TestRunStops(t *testing.T) {
	s := store.New(time.Now)
	d := NewDispatcher(s, slog.New(slog.DiscardHandler), time.Now)
	done := make(chan struct{})
	go func() {
		d.Run(context.Background(), time.Millisecond)
		close(done)
	}()
	d.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run didn't return after Stop")
	}
}
