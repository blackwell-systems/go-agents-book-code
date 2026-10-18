// Command remind runs the reminders service.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata" // zones for weekly schedules, even on minimal images

	"example.com/remind/config"
	"example.com/remind/server"
	"example.com/remind/store"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "(devel)"
	commit  = ""
)

func main() {
	configFile := flag.String("config", "remind.json", "configuration file; missing means defaults")
	flag.Parse()
	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	if err := cfg.ApplyEnv(os.Getenv); err != nil {
		log.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func run(cfg config.Config) error {
	var users []store.User
	if cfg.UsersFile != "" {
		b, err := os.ReadFile(cfg.UsersFile)
		if err != nil {
			return fmt.Errorf("read users: %w", err)
		}
		var raw []struct {
			ID, Name, Email, Token, Plan string
			Admin                        bool
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return fmt.Errorf("read users: %w", err)
		}
		for _, u := range raw {
			users = append(users, store.User{ID: u.ID, Name: u.Name, Email: u.Email, Token: u.Token, Admin: u.Admin, Plan: u.Plan})
		}
	}
	crashes, err := os.OpenFile(filepath.Join(cfg.DataDir, cfg.CrashFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open crash file: %w", err)
	}
	defer crashes.Close()

	srv, err := server.New(server.Config{
		Version: version,
		Commit:  commit,
		Log:     os.Stderr,
		Crashes: crashes,
		Users:   users,
		Snapshot: func() (io.WriteCloser, error) {
			return os.Create(filepath.Join(cfg.DataDir, cfg.SnapshotFile))
		},
		WebhookSecret: []byte(cfg.WebhookSecret),
		RateLimits:    true,
	})
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Dispatcher.Run(ctx, cfg.DispatchInterval)
	go srv.Watch(ctx, 5*time.Second, cfg.StuckAfter)

	hs := &http.Server{Addr: cfg.Addr, Handler: srv.Handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	srv.Log.Info("listening", "addr", cfg.Addr, "version", version)

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Dispatcher.Stop()
	if err := hs.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
