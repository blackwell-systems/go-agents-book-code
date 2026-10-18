package notify_test

import (
	"log/slog"
	"testing"
	"time"

	"example.com/remind/notify"
	"example.com/remind/store"
)

func TestHiddenStopTwice(t *testing.T) {
	d := notify.NewDispatcher(store.New(time.Now), slog.New(slog.DiscardHandler), time.Now)
	defer func() {
		if v := recover(); v != nil {
			t.Errorf("second Stop panicked: %v", v)
		}
	}()
	d.Stop()
	d.Stop()
}
