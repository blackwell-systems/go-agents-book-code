package crash

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LogRing is a slog.Handler that keeps the most recent log lines, so a
// crash report can show what happened just before the crash. It passes
// every record on to another handler.
type LogRing struct {
	next  slog.Handler
	attrs []slog.Attr
	ring  *ring
}

type ring struct {
	now   func() time.Time
	mu    sync.Mutex
	lines []string
	size  int
}

// NewLogRing returns a handler that keeps the last size lines, stamped
// with times from now, and passes records on to next.
func NewLogRing(next slog.Handler, size int, now func() time.Time) *LogRing {
	return &LogRing{next: next, ring: &ring{now: now, size: size}}
}

// Lines returns the kept lines, oldest first.
func (h *LogRing) Lines() []string {
	h.ring.mu.Lock()
	defer h.ring.mu.Unlock()
	return append([]string{}, h.ring.lines...)
}

// Enabled reports whether level is kept (Info and above) or wanted by
// the next handler.
func (h *LogRing) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo || h.next.Enabled(ctx, level)
}

// Handle keeps a text form of r and passes r on.
func (h *LogRing) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(h.ring.now().UTC().Format(time.RFC3339))
	b.WriteByte(' ')
	b.WriteString(r.Level.String())
	b.WriteByte(' ')
	b.WriteString(r.Message)
	write := func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		v := a.Value.Resolve().String()
		if strings.ContainsAny(v, " \"=\n") || v == "" {
			v = strconv.Quote(v)
		}
		b.WriteString(v)
		return true
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)

	h.ring.mu.Lock()
	h.ring.lines = append(h.ring.lines, b.String())
	if over := len(h.ring.lines) - h.ring.size; over > 0 {
		h.ring.lines = h.ring.lines[over:]
	}
	h.ring.mu.Unlock()
	if !h.next.Enabled(ctx, r.Level) {
		return nil
	}
	if err := h.next.Handle(ctx, r); err != nil {
		return fmt.Errorf("log ring: %w", err)
	}
	return nil
}

// WithAttrs returns a handler that adds attrs to every record.
func (h *LogRing) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogRing{
		next:  h.next.WithAttrs(attrs),
		attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...),
		ring:  h.ring,
	}
}

// WithGroup is not supported for kept lines; it only affects next.
func (h *LogRing) WithGroup(name string) slog.Handler {
	return &LogRing{next: h.next.WithGroup(name), attrs: h.attrs, ring: h.ring}
}
