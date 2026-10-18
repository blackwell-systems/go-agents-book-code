// Package config loads remind's configuration from a JSON file and the
// environment.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"time"
)

// Config is remind's configuration. In JSON, durations are strings
// such as "10s" or "1m30s".
type Config struct {
	Addr             string        `json:"addr"`              // listen address
	DataDir          string        `json:"data_dir"`          // directory for the files below
	CrashFile        string        `json:"crash_file"`        // crash reports are appended here
	SnapshotFile     string        `json:"snapshot_file"`     // admin snapshots are written here
	UsersFile        string        `json:"users_file"`        // users to start with; empty for none
	DispatchInterval time.Duration `json:"dispatch_interval"` // how often due reminders are sent
	StuckAfter       time.Duration `json:"stuck_after"`       // when a request in flight gets a crash report
	WebhookSecret    string        `json:"webhook_secret"`    // signs webhook deliveries; empty for unsigned
	DefaultPlan      string        `json:"default_plan"`      // plan for new users: "free" or "pro"
	LogLevel         string        `json:"log_level"`         // debug, info, warn, or error
}

// Plans and log levels Validate accepts.
var (
	plans     = []string{"free", "pro"}
	logLevels = []string{"debug", "info", "warn", "error"}
)

// MinSecretLen is the shortest webhook secret Validate accepts.
const MinSecretLen = 16

// Default returns the configuration used when nothing overrides it.
func Default() Config {
	return Config{
		Addr:             ":8080",
		DataDir:          ".",
		CrashFile:        "crashes.jsonl",
		SnapshotFile:     "snapshot.json",
		DispatchInterval: 10 * time.Second,
		StuckAfter:       30 * time.Second,
		DefaultPlan:      "free",
		LogLevel:         "info",
	}
}

// Load reads a JSON configuration file over the defaults. A missing
// file gives the defaults; unknown fields are an error. Load does not
// validate the result.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("load config %s: %w", path, err)
	}
	return cfg, nil
}

// configJSON is Config with its durations as strings.
type configJSON struct {
	*plain
	DispatchInterval string `json:"dispatch_interval"`
	StuckAfter       string `json:"stuck_after"`
}

type plain Config

// MarshalJSON encodes c with durations as strings.
func (c Config) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(configJSON{
		plain:            (*plain)(&c),
		DispatchInterval: c.DispatchInterval.String(),
		StuckAfter:       c.StuckAfter.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return b, nil
}

// UnmarshalJSON decodes fields present in b over c, rejecting unknown
// fields.
func (c *Config) UnmarshalJSON(b []byte) error {
	v := configJSON{
		plain:            (*plain)(c),
		DispatchInterval: c.DispatchInterval.String(),
		StuckAfter:       c.StuckAfter.String(),
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	var errs []error
	for _, d := range []struct {
		name string
		s    string
		dst  *time.Duration
	}{
		{"dispatch_interval", v.DispatchInterval, &c.DispatchInterval},
		{"stuck_after", v.StuckAfter, &c.StuckAfter},
	} {
		dur, err := time.ParseDuration(d.s)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.name, err))
			continue
		}
		*d.dst = dur
	}
	return errors.Join(errs...)
}

// ApplyEnv overrides c with REMIND_* environment variables read through
// getenv. Unset or empty variables leave the field alone. The variables
// are REMIND_ADDR, REMIND_DATA_DIR, REMIND_CRASH_FILE,
// REMIND_SNAPSHOT_FILE, REMIND_USERS_FILE, REMIND_DISPATCH_INTERVAL,
// REMIND_STUCK_AFTER, REMIND_WEBHOOK_SECRET, REMIND_DEFAULT_PLAN, and
// REMIND_LOG_LEVEL.
func (c *Config) ApplyEnv(getenv func(string) string) error {
	for _, s := range []struct {
		env string
		dst *string
	}{
		{"REMIND_ADDR", &c.Addr},
		{"REMIND_DATA_DIR", &c.DataDir},
		{"REMIND_CRASH_FILE", &c.CrashFile},
		{"REMIND_SNAPSHOT_FILE", &c.SnapshotFile},
		{"REMIND_USERS_FILE", &c.UsersFile},
		{"REMIND_WEBHOOK_SECRET", &c.WebhookSecret},
		{"REMIND_DEFAULT_PLAN", &c.DefaultPlan},
		{"REMIND_LOG_LEVEL", &c.LogLevel},
	} {
		if v := getenv(s.env); v != "" {
			*s.dst = v
		}
	}

	var errs []error
	for _, d := range []struct {
		env string
		dst *time.Duration
	}{
		{"REMIND_DISPATCH_INTERVAL", &c.DispatchInterval},
		{"REMIND_STUCK_AFTER", &c.StuckAfter},
	} {
		v := getenv(d.env)
		if v == "" {
			continue
		}
		dur, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.env, err))
			continue
		}
		*d.dst = dur
	}
	return errors.Join(errs...)
}

// Validate reports every problem with c, joined into one error.
func (c *Config) Validate() error {
	var errs []error
	if c.Addr == "" {
		errs = append(errs, errors.New("addr is empty"))
	}
	if c.DispatchInterval <= 0 {
		errs = append(errs, fmt.Errorf("dispatch_interval must be positive, got %v", c.DispatchInterval))
	}
	if c.StuckAfter <= 0 {
		errs = append(errs, fmt.Errorf("stuck_after must be positive, got %v", c.StuckAfter))
	}
	if !slices.Contains(plans, c.DefaultPlan) {
		errs = append(errs, fmt.Errorf("default_plan %q is not one of %q", c.DefaultPlan, plans))
	}
	if n := len(c.WebhookSecret); n > 0 && n < MinSecretLen {
		errs = append(errs, fmt.Errorf("webhook_secret is %d bytes, want at least %d", n, MinSecretLen))
	}
	if !slices.Contains(logLevels, c.LogLevel) {
		errs = append(errs, fmt.Errorf("log_level %q is not one of %q", c.LogLevel, logLevels))
	}
	return errors.Join(errs...)
}
