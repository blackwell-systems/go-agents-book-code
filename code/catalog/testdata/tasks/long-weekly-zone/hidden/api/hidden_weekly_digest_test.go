package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"example.com/remind/store"
)

func TestHiddenDigestWestOfUTC(t *testing.T) {
	now := time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC) // Monday 10:00 in Los Angeles
	srv := hiddenServer(t, now, store.User{ID: "h_eli", Token: "h-tok-eli", Prefs: &store.Prefs{TZ: "America/Los_Angeles", Channel: "email"}})
	hiddenCreate(t, srv, "h-tok-eli", `{"text":"call mom","every":"on sun at 20:00"}`)
	code, body := hiddenDo(t, srv, "h-tok-eli", http.MethodGet, "/v1/me/digest", "")
	if code != http.StatusOK {
		t.Fatalf("digest: status %d: %s", code, body)
	}
	var d struct {
		Days []struct {
			Date  string
			Items []json.RawMessage
		}
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	for _, day := range d.Days {
		if n := len(day.Items); (day.Date == "2026-10-18") != (n == 1) || n > 1 {
			t.Errorf("%s: %d items", day.Date, n)
		}
	}
}
