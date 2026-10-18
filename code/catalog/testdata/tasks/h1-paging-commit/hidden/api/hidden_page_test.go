package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHiddenPagePastEnd(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	hiddenCreate(t, srv, "h-tok-ana", `{"text":"x","in":"1h"}`)
	code, body := hiddenDo(t, srv, "h-tok-ana", http.MethodGet, "/v1/reminders?offset=40", "")
	if code != http.StatusOK || !strings.Contains(body, `"reminders":[]`) {
		t.Errorf("offset past the end: status %d, body %s; want 200 and no reminders", code, body)
	}
}
