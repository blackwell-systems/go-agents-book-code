// Package notify delivers reminders through each user's chosen channel.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"example.com/remind/i18n"
	"example.com/remind/store"
	"example.com/remind/webhook"
)

// A Message is one notification.
type Message struct {
	To      *store.User
	Subject string
	Body    string
}

// A Sender delivers messages over one channel.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// channels lists the channel names users may choose.
var channels = []string{"email", "webhook", "sms"}

// ValidChannel reports whether name is a channel users may choose.
func ValidChannel(name string) bool {
	return slices.Contains(channels, name)
}

// Dispatcher sends due reminders.
type Dispatcher struct {
	store   *store.Store
	senders map[string]Sender
	log     *slog.Logger
	now     func() time.Time

	mu      sync.Mutex
	retries map[string]int // failed sends per reminder ID
	stop    chan struct{}
}

// NewDispatcher returns a dispatcher with the email and webhook channels.
func NewDispatcher(s *store.Store, log *slog.Logger, now func() time.Time) *Dispatcher {
	d := &Dispatcher{
		store: s,
		senders: map[string]Sender{
			"email":   emailSender{log: log},
			"webhook": webhookSender{client: &webhook.Client{Log: log, Now: now}, now: now},
		},
		log:     log,
		now:     now,
		retries: make(map[string]int),
		stop:    make(chan struct{}),
	}
	s.OnChange(d.forget)
	return d
}

// SetWebhookSecret sets the key webhook deliveries are signed with.
func (d *Dispatcher) SetWebhookSecret(secret []byte) {
	d.senders["webhook"].(webhookSender).client.Secret = secret
}

// Status is what the admin dashboard shows about deliveries.
type Status struct {
	Retrying int      `json:"retrying"` // reminders whose last send failed
	Channels []string `json:"channels"`
}

// Status reports the dispatcher's current state.
func (d *Dispatcher) Status() Status {
	return Status{Retrying: len(d.retries), Channels: slices.Sorted(maps.Keys(d.senders))}
}

// Run sends due reminders every interval until Stop is called or ctx
// ends.
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stop:
			return
		case <-t.C:
			d.flush(ctx)
		}
	}
}

// Stop stops Run.
func (d *Dispatcher) Stop() {
	close(d.stop)
}

// flush sends every due reminder once. It holds d.mu so flushes never
// overlap.
func (d *Dispatcher) flush(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.store.Due(d.now()) {
		u, err := d.store.User(r.UserID)
		if err != nil {
			continue
		}
		if err := d.SendNow(ctx, u, r); err != nil {
			d.retries[r.ID]++
			d.log.Warn("notify: send failed", "reminder", r.ID, "attempt", d.retries[r.ID], "err", err)
			continue
		}
		delete(d.retries, r.ID)
		d.store.MarkSent(r.ID)
	}
}

// forget drops retry state for a reminder that changed.
func (d *Dispatcher) forget(r *store.Reminder) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.retries, r.ID)
}

// SendNow sends one reminder to its user immediately.
func (d *Dispatcher) SendNow(ctx context.Context, u *store.User, r *store.Reminder) error {
	if err := d.senders[u.Prefs.Channel].Send(ctx, message(u, r)); err != nil {
		return fmt.Errorf("send %s: %w", r.ID, err)
	}
	return nil
}

// message renders a reminder in the user's language.
func message(u *store.User, r *store.Reminder) Message {
	lang := u.Prefs.Lang
	return Message{
		To:      u,
		Subject: i18n.Translate(lang, "reminder.subject", 0, map[string]string{"text": Preview(r.Text)}),
		Body:    i18n.Translate(lang, "reminder.body", 0, map[string]string{"name": u.Name, "text": r.Text}),
	}
}

type emailSender struct {
	log *slog.Logger
}

func (s emailSender) Send(_ context.Context, m Message) error {
	// The mail relay picks messages up from the log stream.
	s.log.Info("notify: email queued", "to", m.To.Email, "subject", m.Subject)
	return nil
}

type webhookSender struct {
	client *webhook.Client
	now    func() time.Time
}

func (s webhookSender) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(map[string]string{"subject": m.Subject, "body": m.Body})
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	d := &webhook.Delivery{
		ID:      fmt.Sprintf("whd_%d", s.now().UnixNano()),
		URL:     m.To.Prefs.Webhook,
		Event:   "reminder.due",
		Payload: body,
		Created: s.now(),
	}
	if err := s.client.Post(ctx, d); err != nil {
		return fmt.Errorf("webhook to %s: %w", d.URL, err)
	}
	return nil
}
