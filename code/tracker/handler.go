package tracker

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// NewHandler serves the tracker's HTTP API:
//
//	POST  /issues               {"title", "body", "labels", "fingerprint"}
//	GET   /issues?q=&fingerprint=&status=&key=
//	GET   /issues/{id}
//	POST  /issues/{id}/comments {"body"}
//	PATCH /issues/{id}          {"status"}
//
// The two POSTs take an Idempotency-Key header (chapter 15), whose
// value is a quoted string, as the IETF draft has it: "8e03978e". A
// request that repeats one already done with the same key does nothing
// more and gets 200 with the same issue, where the first got 201 (a
// choice: the draft asks for the first response, and Stripe adds a
// header to say it is a replay). A key reused for a different request
// gets 422; a key that isn't a quoted string gets 400. A repeat that
// arrives while the first is still running waits for it, since the
// store holds one lock for the whole create, so there is no 409.
func NewHandler(s *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /issues", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Title       string   `json:"title"`
			Body        string   `json:"body"`
			Labels      []string `json:"labels"`
			Fingerprint string   `json:"fingerprint"`
		}
		if !decode(w, r, &req) {
			return
		}
		key, ok := keyFrom(w, r)
		if !ok {
			return
		}
		is, created, err := s.CreateOnce(key, req.Title, req.Body,
			req.Labels, req.Fingerprint)
		reply(w, createdStatus(created), is, err)
	})
	mux.HandleFunc("GET /issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		issues := s.Search(Query{Text: q.Get("q"), Fingerprint: q.Get("fingerprint"),
			Status: q.Get("status"), Key: q.Get("key")})
		reply(w, http.StatusOK, map[string]any{"issues": issues}, nil)
	})
	mux.HandleFunc("GET /issues/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := issueID(w, r)
		if !ok {
			return
		}
		is, err := s.Get(id)
		reply(w, http.StatusOK, is, err)
	})
	mux.HandleFunc("POST /issues/{id}/comments", func(w http.ResponseWriter, r *http.Request) {
		id, ok := issueID(w, r)
		if !ok {
			return
		}
		var req struct {
			Body string `json:"body"`
		}
		if !decode(w, r, &req) {
			return
		}
		key, ok := keyFrom(w, r)
		if !ok {
			return
		}
		is, created, err := s.CommentOnce(id, key, req.Body)
		reply(w, createdStatus(created), is, err)
	})
	mux.HandleFunc("PATCH /issues/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := issueID(w, r)
		if !ok {
			return
		}
		var req struct {
			Status string `json:"status"`
		}
		if !decode(w, r, &req) {
			return
		}
		is, err := s.SetStatus(id, req.Status)
		reply(w, http.StatusOK, is, err)
	})
	return mux
}

// KeyHeader is the request header that carries an idempotency key.
const KeyHeader = "Idempotency-Key"

// keyFrom reads the request's idempotency key, a Structured Field
// string (RFC 8941): printable ASCII in double quotes, with \" and \\
// as the only escapes. No header is no key.
func keyFrom(w http.ResponseWriter, r *http.Request) (string, bool) {
	h := r.Header.Get(KeyHeader)
	if h == "" {
		return "", true
	}
	key, err := unquote(h)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": KeyHeader + ": " + err.Error()})
		return "", false
	}
	return key, true
}

func unquote(h string) (string, error) {
	if len(h) < 2 || h[0] != '"' || h[len(h)-1] != '"' {
		return "", errors.New(`want a quoted string, such as "abc"`)
	}
	var b strings.Builder
	for i := 1; i < len(h)-1; i++ {
		c := h[i]
		switch {
		case c == '\\' && i+1 < len(h)-1 &&
			(h[i+1] == '"' || h[i+1] == '\\'):
			i++
			b.WriteByte(h[i])
		case c == '\\' || c == '"' || c < 0x20 || c > 0x7e:
			return "", fmt.Errorf("bad character at %d", i)
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

// createdStatus is 201 for a request that made something, and 200 for
// one that repeated a request already done.
func createdStatus(created bool) int {
	if created {
		return http.StatusCreated
	}
	return http.StatusOK
}

func issueID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		reply(w, 0, nil, ErrNotFound)
		return 0, false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 1<<20), v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return false
	}
	return true
}

// reply writes v with status, or the status and message for err.
func reply(w http.ResponseWriter, status int, v any, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrKeyReused):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, status, v)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is already sent; a failed write means the client left.
	_ = json.MarshalWrite(w, v)
}
