package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHiddenQuietHoursRange(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 10, 21, 0, 0, 0, time.UTC))
	for _, body := range []string{`{"quiet_from":"24:30","quiet_to":"07:00"}`, `{"quiet_from":"22:00","quiet_to":"07:60"}`} {
		if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodPut, "/v1/me/prefs", body); code != http.StatusBadRequest {
			t.Errorf("prefs %s: status %d, want 400: %s", body, code, resp)
		}
	}
	if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodPut, "/v1/me/prefs", `{"quiet_from":"22:00","quiet_to":"07:00"}`); code != http.StatusOK {
		t.Fatalf("prefs 22:00 to 07:00: status %d: %s", code, resp)
	}
	code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodGet, "/v1/me/digest", "")
	if code != http.StatusOK || !strings.Contains(resp, `"quiet_hours":[0,1,2,3,4,5,6,22,23]`) {
		t.Errorf("digest: status %d: %s", code, resp)
	}
}
