package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestHiddenUnknownDayAPI(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodPost, "/v1/reminders", `{"text":"x","every":"on funday at 09:00"}`); code != http.StatusBadRequest {
		t.Errorf("unknown day: status %d, want 400: %s", code, resp)
	}
}
