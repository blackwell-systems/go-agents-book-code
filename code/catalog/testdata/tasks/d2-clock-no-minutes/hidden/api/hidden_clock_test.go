package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestHiddenClockWithoutMinutes(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	for _, spec := range []string{"on mon at 9", "on mon at 09", "on mon at "} {
		body := `{"text":"x","every":"` + spec + `"}`
		if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodPost, "/v1/reminders", body); code != http.StatusBadRequest {
			t.Errorf("every %q: status %d, want 400: %s", spec, code, resp)
		}
	}
	hiddenCreate(t, srv, "h-tok-ana", `{"text":"x","every":"on mon at 9:05"}`)
}
