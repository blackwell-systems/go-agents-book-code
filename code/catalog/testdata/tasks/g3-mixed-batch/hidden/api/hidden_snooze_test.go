package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestHiddenSnoozeEmpty(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	id := hiddenCreate(t, srv, "h-tok-ana", `{"text":"x","in":"1h"}`)
	if code, body := hiddenDo(t, srv, "h-tok-ana", http.MethodPost, "/v1/reminders/"+id+"/snooze", `{"for":""}`); code != http.StatusBadRequest {
		t.Errorf("snooze for \"\": status %d, want 400: %s", code, body)
	}
}
