package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHiddenPriorityRange(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	for _, p := range []string{"-1", "0", "4"} {
		body := `{"text":"x","in":"1h","priority":` + p + `}`
		if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodPost, "/v1/reminders", body); code != http.StatusBadRequest {
			t.Errorf("priority %s: status %d, want 400: %s", p, code, resp)
		}
	}
	hiddenCreate(t, srv, "h-tok-ana", `{"text":"y","in":"1h","priority":1}`)
	code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodGet, "/v1/reminders", "")
	if code != http.StatusOK || !strings.Contains(resp, `"priority":"high"`) {
		t.Errorf("list: status %d: %s", code, resp)
	}
}
