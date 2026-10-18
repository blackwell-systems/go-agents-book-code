package api

import (
	"net/http"
	"strconv"
	"time"

	"example.com/remind/auth"
	"example.com/remind/calendar"
	"example.com/remind/schedule"
)

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	q := r.URL.Query().Get("q")
	if q == "" {
		a.fail(w, http.StatusBadRequest, "q is required")
		return
	}
	limit := 20
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			a.fail(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = n
	}
	var views []reminderView
	for _, hit := range a.Search.Search(u.ID, q, limit) {
		rem, err := a.Store.Get(hit.DocID)
		if err != nil {
			continue
		}
		views = append(views, viewOf(rem))
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"reminders": views})
}

// calendarFeed serves the caller's reminders as an iCalendar file that
// calendar apps can subscribe to.
func (a *API) calendarFeed(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var events []calendar.Event
	for _, rem := range a.Store.List(u.ID, 0, 1000) {
		if rem.Done {
			continue
		}
		ev := calendar.Event{
			UID:      rem.ID + "@remind.example",
			Summary:  rem.Text,
			Start:    rem.Due,
			Duration: 15 * time.Minute,
		}
		switch rule := rem.Rule.(type) {
		case schedule.Weekly:
			ev.RRule = calendar.WeeklyRRule(rule.Days)
		case schedule.Every:
			ev.RRule = calendar.IntervalRRule(rule.Interval)
		}
		events = append(events, ev)
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	cw := calendar.Writer{Now: a.Now}
	if err := cw.Write(w, "-//remind//reminders//EN", events); err != nil {
		a.Log.Warn("write calendar", "user", u.ID, "err", err)
	}
}
