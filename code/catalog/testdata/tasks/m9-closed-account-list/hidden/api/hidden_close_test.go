package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHiddenClosedAccountLists(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	newList := func(token, name string) string {
		code, resp := hiddenDo(t, srv, token, http.MethodPost, "/v1/lists", `{"name":"`+name+`"}`)
		if code != http.StatusCreated {
			t.Fatalf("create list: status %d: %s", code, resp)
		}
		var l struct{ ID string }
		if err := json.Unmarshal([]byte(resp), &l); err != nil {
			t.Fatal(err)
		}
		return l.ID
	}
	bos := newList("h-tok-bo", "Groceries")
	anas := newList("h-tok-ana", "Trip")
	for _, add := range []struct{ token, list, user string }{{"h-tok-bo", bos, "h_ana"}, {"h-tok-ana", anas, "h_bo"}} {
		if code, resp := hiddenDo(t, srv, add.token, http.MethodPost, "/v1/lists/"+add.list+"/members", `{"user":"`+add.user+`","role":"viewer"}`); code != http.StatusNoContent {
			t.Fatalf("add member: status %d: %s", code, resp)
		}
	}
	if code, resp := hiddenDo(t, srv, "h-tok-bo", http.MethodDelete, "/v1/me", ""); code != http.StatusNoContent {
		t.Fatalf("close account: status %d: %s", code, resp)
	}
	if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodGet, "/v1/lists/"+bos, ""); code != http.StatusNotFound {
		t.Errorf("closed user's list: status %d, want 404: %s", code, resp)
	}
	code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodGet, "/v1/lists/"+anas, "")
	if code != http.StatusOK || strings.Contains(resp, "h_bo") {
		t.Errorf("Ana's list after Bo left: status %d: %s", code, resp)
	}
}
