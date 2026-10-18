package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json/v2"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// tag::routes[]

// Handler returns the service's HTTP API. remind posts reports with
// the ingest secret; everything else needs a reviewer's token. It
// serves JSON only, to the review command; a review page in a browser
// would also need sessions, CSRF protection, and escaped output.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /crashes", s.postCrash)
	mux.Handle("GET /runs", s.reviewer(s.listRuns))
	mux.Handle("GET /runs/{id}", s.reviewer(s.getRun))
	mux.Handle("GET /review", s.reviewer(s.listReview))
	mux.Handle("POST /review/{id}/approve",
		s.reviewer(s.decision(KindApproval, true)))
	mux.Handle("POST /review/{id}/deny",
		s.reviewer(s.decision(KindApproval, false)))
	// An unknown outcome gets its own answers: rerun the call, or
	// record it as done with what the person found (chapter 15).
	mux.Handle("POST /review/{id}/rerun",
		s.reviewer(s.decision(KindOutcome, true)))
	mux.Handle("POST /review/{id}/done",
		s.reviewer(s.decision(KindOutcome, false)))
	mux.Handle("GET /results", s.reviewer(s.results))
	return mux
}

// end::routes[]

// tag::post-crash[]

// postCrash takes one report from remind. It answers 202 only once the
// report is on disk, 200 for a report ID it has seen, and 503 with
// Retry-After when the queue is full, so remind's retry comes later.
func (s *Service) postCrash(w http.ResponseWriter, r *http.Request) {
	if !sameSecret(bearer(r), s.cfg.Secret) {
		fail(w, http.StatusUnauthorized, "wrong or missing secret")
		return
	}
	if ok, wait := s.limit.allow(source(r)); !ok {
		w.Header().Set("Retry-After", retryAfter(wait))
		fail(w, http.StatusTooManyRequests, "too many reports")
		return
	}

	// end::post-crash[]

	// tag::post-crash-read[]

	var wr wireReport
	body := http.MaxBytesReader(w, r.Body, maxReport)
	if err := json.UnmarshalRead(body, &wr); err != nil {
		code, msg := http.StatusBadRequest, "not a crash report"
		if _, big := errors.AsType[*http.MaxBytesError](err); big {
			code, msg = http.StatusRequestEntityTooLarge,
				"report too large"
		}
		fail(w, code, msg)
		return
	}
	if err := wr.validate(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	rep := wr.report() // redacted from here on
	got, err := s.ingest(r.Context(), storedReport{Report: rep,
		Fingerprint: triage.Fingerprint(rep), Received: s.cfg.Now()})
	switch {
	case errors.Is(err, ErrFull):
		w.Header().Set("Retry-After", "60")
		fail(w, http.StatusServiceUnavailable, "queue full")
	case err != nil:
		s.cfg.Log.Error("ingest", "report", rep.ID, "err", err)
		fail(w, http.StatusInternalServerError, "report not saved")
	case got.Duplicate:
		reply(w, http.StatusOK, got)
	default:
		reply(w, http.StatusAccepted, got)
	}

	// end::post-crash-read[]
}

type nameKey struct{}

// tag::reviewer[]

// reviewer lets a request through only with a reviewer's token, and
// passes on the reviewer's name, which every decision records. Every
// token is compared, so the time taken doesn't say which one nearly
// matched.
func (s *Service) reviewer(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		got := sha256.Sum256([]byte(bearer(r)))
		name := ""
		for tok, who := range s.cfg.Reviewers {
			want := sha256.Sum256([]byte(tok))
			if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
				name = who
			}
		}
		if name == "" {
			fail(w, http.StatusUnauthorized, "not a reviewer")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), nameKey{},
			name)))
	})
}

// end::reviewer[]

// A Status is a run as the status endpoints report it: the saved run,
// and how many review items it waits for, stale ones counted apart.
type Status struct {
	Run     `json:",inline"`
	Waiting int `json:"waiting,omitzero"`
	Stale   int `json:"stale,omitzero"`
}

func (s *Service) status(r Run) Status {
	st := Status{Run: r}
	st.Waiting, st.Stale = s.review.waiting(r.ID)
	return st
}

func (s *Service) listRuns(w http.ResponseWriter, _ *http.Request) {
	runs := s.snapshot()
	out := make([]Status, len(runs))
	for i, r := range runs {
		out[i] = s.status(r)
	}
	reply(w, http.StatusOK, out)
}

func (s *Service) getRun(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	run, ok := s.runs[r.PathValue("id")]
	var out Run
	if ok {
		out = *run
	}
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "no such run")
		return
	}
	reply(w, http.StatusOK, s.status(out))
}

func (s *Service) listReview(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, s.review.open())
}

var itemPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// tag::decision[]

// decision records a reviewer's answer on an item and wakes its run
// once nothing else holds it. A denial needs a reason: the model
// reads it, and for an unknown outcome it is the call's result.
func (s *Service) decision(kind string, approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !itemPattern.MatchString(id) {
			fail(w, http.StatusNotFound, "no such item")
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 4<<10),
			&body)
		if err != nil {
			fail(w, http.StatusBadRequest,
				`body: {"reason": "..."}`)
			return
		}
		by, _ := r.Context().Value(nameKey{}).(string)

		// end::decision[]

		// tag::decision-reply[]

		it, err := s.review.decide(id, kind, Decision{Approved: approve,
			By: by, Reason: strings.TrimSpace(body.Reason),
			At: s.cfg.Now()})
		switch {
		case errors.Is(err, ErrNoItem):
			fail(w, http.StatusNotFound, "no such item")
		case errors.Is(err, ErrNoReason):
			fail(w, http.StatusBadRequest, "a denial needs a reason")
		case errors.Is(err, ErrDecided), errors.Is(err, ErrStaleItem),
			errors.Is(err, ErrWrongKind), errors.Is(err, ErrClosed):
			fail(w, http.StatusConflict, err.Error())
		case err != nil:
			s.cfg.Log.Error("decide", "item", id, "err", err)
			fail(w, http.StatusInternalServerError,
				"decision not saved")
		default:
			s.wake(it.Run)
			s.poke() // a late-report comment may have been decided
			reply(w, http.StatusOK, it)
		}
	}
}

// end::decision-reply[]

// A Results is the service's report: each group's run, and how many
// ended each way.
type Results struct {
	Runs       []Run          `json:"runs"`
	Counts     map[string]int `json:"counts"`
	Stale      int            `json:"stale"` // unknown outcomes waiting
	SpentToday float64        `json:"spent_today"`
}

func (s *Service) results(w http.ResponseWriter, _ *http.Request) {
	res := Results{Runs: s.snapshot(), Counts: map[string]int{}}
	for _, r := range res.Runs {
		res.Counts[cmp.Or(r.Outcome, string(r.State))]++
		_, stale := s.review.waiting(r.ID)
		res.Stale += stale
	}
	s.mu.Lock()
	res.SpentToday = s.ledger.Spent
	s.mu.Unlock()
	reply(w, http.StatusOK, res)
}

// snapshot returns a copy of every run, oldest first.
func (s *Service) snapshot() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b Run) int {
		return a.Created.Compare(b.Created)
	})
	return out
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, v) // the client may be gone; nothing to do
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}
