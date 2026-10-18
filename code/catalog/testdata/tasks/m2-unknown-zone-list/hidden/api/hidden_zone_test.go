package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestHiddenUnknownZone(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	body := `{"text":"x","every":"on mon at 09:00","tz":"Europe/Pariss"}`
	if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodPost, "/v1/reminders", body); code != http.StatusBadRequest {
		t.Errorf("unknown zone: status %d, want 400: %s", code, resp)
	}
	hiddenCreate(t, srv, "h-tok-ana", `{"text":"y","every":"on mon at 09:00","tz":"Europe/Paris"}`)
	if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodGet, "/v1/reminders", ""); code != http.StatusOK {
		t.Errorf("list: status %d: %s", code, resp)
	}
}
