package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestHiddenUnsupportedChannel(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	code, _ := hiddenDo(t, srv, "h-tok-bo", http.MethodPut, "/v1/me/prefs", `{"channel":"sms"}`)
	if code == http.StatusOK {
		id := hiddenCreate(t, srv, "h-tok-bo", `{"text":"x","in":"1h"}`)
		code, resp := hiddenDo(t, srv, "h-tok-bo", http.MethodPost, "/v1/reminders/"+id+"/test", "")
		if code >= 500 && code != http.StatusBadGateway {
			t.Errorf("test notification over sms: status %d: %s", code, resp)
		}
		return
	}
	if code != http.StatusBadRequest {
		t.Errorf("channel sms: status %d, want 400 or a working channel", code)
	}
}
