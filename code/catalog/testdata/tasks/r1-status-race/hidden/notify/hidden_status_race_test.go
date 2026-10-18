package notify_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"example.com/remind/notify"
	"example.com/remind/schedule"
	"example.com/remind/store"
)

func TestHiddenStatusWhileSending(t *testing.T) {
	s := store.New(time.Now)
	prefs := &store.Prefs{Channel: "webhook", Webhook: "https://127.0.0.1:1/hook"}
	if err := s.AddUser(store.User{ID: "u1", Email: "u1@example.com", Prefs: prefs}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		rule, err := schedule.Parse("every 1s", nil, time.Now().Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		s.Create("u1", "x", time.Time{}, rule)
	}
	d := notify.NewDispatcher(s, slog.New(slog.DiscardHandler), func() time.Time { return time.Now().Add(time.Hour) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx, 200*time.Microsecond)
	for stop := time.Now().Add(time.Second); time.Now().Before(stop); {
		_ = d.Status()
	}
}
