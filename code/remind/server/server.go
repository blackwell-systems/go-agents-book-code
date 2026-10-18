// Package server assembles remind from its parts.
package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"example.com/remind/api"
	"example.com/remind/audit"
	"example.com/remind/crash"
	"example.com/remind/metrics"
	"example.com/remind/notify"
	"example.com/remind/quota"
	"example.com/remind/search"
	"example.com/remind/share"
	"example.com/remind/store"
)

// Config configures a Server. Zero values get defaults.
type Config struct {
	Version  string
	Commit   string
	Now      func() time.Time               // default time.Now
	Log      io.Writer                      // default io.Discard
	Crashes  io.Writer                      // where crash reports go; default io.Discard
	Users    []store.User                   // users to start with
	Snapshot func() (io.WriteCloser, error) // where admin snapshots go
	// WebhookSecret signs webhook deliveries.
	WebhookSecret []byte
	// RateLimits turns on per-user rate limits.
	RateLimits bool
}

// A Server is a configured remind service.
type Server struct {
	Handler    http.Handler
	Store      *store.Store
	Dispatcher *notify.Dispatcher
	Reporter   *crash.Reporter
	Log        *slog.Logger
}

// New builds a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	if cfg.Crashes == nil {
		cfg.Crashes = io.Discard
	}
	ring := crash.NewLogRing(slog.NewTextHandler(cfg.Log, nil), 12, cfg.Now)
	log := slog.New(ring)

	st := store.New(cfg.Now)
	for _, u := range cfg.Users {
		if err := st.AddUser(u); err != nil {
			return nil, fmt.Errorf("server: %w", err)
		}
	}
	d := notify.NewDispatcher(st, log, cfg.Now)

	rep := crash.NewReporter(cfg.Crashes, cfg.Now)
	rep.Version = cfg.Version
	rep.Commit = cfg.Commit
	rep.Logs = ring

	d.SetWebhookSecret(cfg.WebhookSecret)

	reg := metrics.NewRegistry()
	reg.Now = cfg.Now
	a := &api.API{
		Store:    st,
		Notify:   d,
		Lists:    share.New(cfg.Now),
		Search:   search.New(),
		Audit:    audit.New(1000, cfg.Now),
		Metrics:  reg.Handler(),
		Log:      log,
		Now:      cfg.Now,
		Snapshot: cfg.Snapshot,
	}
	if cfg.RateLimits {
		a.Limiter = quota.NewLimiter(cfg.Now)
	}
	return &Server{
		Handler:    rep.Middleware(metrics.Instrument(reg, accessLog(log, cfg.Now, a.Routes()))),
		Store:      st,
		Dispatcher: d,
		Reporter:   rep,
		Log:        log,
	}, nil
}

// Watch writes a crash report for any request in flight longer than
// limit, checking every interval until ctx ends.
func (s *Server) Watch(ctx context.Context, interval, limit time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reporter.Check(limit)
		}
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func accessLog(log *slog.Logger, now func() time.Time, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"dur", now().Sub(start).String(), "ua", r.UserAgent())
	})
}
