package notify_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"example.com/remind/notify"
	"example.com/remind/schedule"
	"example.com/remind/store"
)

func TestHiddenFlushAndUpdate(t *testing.T) {
	s := store.New(time.Now)
	if err := s.AddUser(store.User{ID: "u1", Email: "u1@example.com"}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 5 {
		rule, err := schedule.Parse("every 1s", nil, time.Now().Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.Create("u1", "x", time.Time{}, rule).ID)
	}
	d := notify.NewDispatcher(s, slog.New(slog.DiscardHandler), func() time.Time { return time.Now().Add(time.Hour) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx, 100*time.Microsecond)

	// Update reminders while the dispatcher sends them, for long enough
	// that the two contend for locks many times.
	stop := time.Now().Add(1500 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, id := range ids {
			wg.Go(func() {
				for time.Now().Before(stop) {
					s.Update(id, func(r *store.Reminder) error { r.Due = time.Now(); return nil })
				}
			})
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("updates stopped finishing while the dispatcher ran: deadlock")
	}
}
