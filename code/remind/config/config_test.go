package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Errorf("Default().Validate() = %v", err)
	}
	if c.Addr != ":8080" || c.DispatchInterval != 10*time.Second || c.DefaultPlan != "free" {
		t.Errorf("Default() = %+v", c)
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "remind.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeFile(t, `{
		"addr": ":9090",
		"data_dir": "/var/lib/remind",
		"dispatch_interval": "1m30s",
		"default_plan": "pro"
	}`)
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Addr = ":9090"
	want.DataDir = "/var/lib/remind"
	want.DispatchInterval = 90 * time.Second
	want.DefaultPlan = "pro"
	if got != want {
		t.Errorf("Load:\n got %+v\nwant %+v", got, want)
	}
}

func TestLoadMissing(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got != Default() {
		t.Errorf("Load of a missing file = %+v, want defaults", got)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name, content, want string
	}{
		{"unknown field", `{"listen": ":9090"}`, `unknown field "listen"`},
		{"bad duration", `{"stuck_after": "soon"}`, "stuck_after"},
		{"wrong type", `{"addr": 9090}`, "addr"},
		{"syntax", `{"addr": ":9090",}`, "invalid character"},
		{"trailing data", `{} {}`, "invalid character"},
	}
	for _, tt := range tests {
		_, err := Load(writeFile(t, tt.content))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Load error = %v, want one mentioning %q", tt.name, err, tt.want)
		}
	}
}

func TestJSONRoundTrip(t *testing.T) {
	c := Default()
	c.StuckAfter = 2 * time.Minute
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"stuck_after":"2m0s"`) {
		t.Errorf("Marshal = %s, want durations as strings", b)
	}
	var got Config
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, c)
	}
}

func TestApplyEnv(t *testing.T) {
	env := map[string]string{
		"REMIND_ADDR":              "127.0.0.1:7000",
		"REMIND_DATA_DIR":          "/data",
		"REMIND_CRASH_FILE":        "c.jsonl",
		"REMIND_SNAPSHOT_FILE":     "s.json",
		"REMIND_USERS_FILE":        "u.json",
		"REMIND_DISPATCH_INTERVAL": "2s",
		"REMIND_STUCK_AFTER":       "1m",
		"REMIND_WEBHOOK_SECRET":    "0123456789abcdef",
		"REMIND_DEFAULT_PLAN":      "pro",
		"REMIND_LOG_LEVEL":         "debug",
	}
	c := Default()
	if err := c.ApplyEnv(func(string) string { return "" }); err != nil || c != Default() {
		t.Errorf("ApplyEnv with nothing set: %+v, %v", c, err)
	}
	if err := c.ApplyEnv(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	want := Config{
		Addr:             "127.0.0.1:7000",
		DataDir:          "/data",
		CrashFile:        "c.jsonl",
		SnapshotFile:     "s.json",
		UsersFile:        "u.json",
		DispatchInterval: 2 * time.Second,
		StuckAfter:       time.Minute,
		WebhookSecret:    "0123456789abcdef",
		DefaultPlan:      "pro",
		LogLevel:         "debug",
	}
	if c != want {
		t.Errorf("ApplyEnv:\n got %+v\nwant %+v", c, want)
	}
}

func TestApplyEnvErrors(t *testing.T) {
	env := map[string]string{
		"REMIND_ADDR":              ":9999",
		"REMIND_DISPATCH_INTERVAL": "often",
		"REMIND_STUCK_AFTER":       "30",
	}
	c := Default()
	err := c.ApplyEnv(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("ApplyEnv accepted bad durations")
	}
	for _, name := range []string{"REMIND_DISPATCH_INTERVAL", "REMIND_STUCK_AFTER"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s", err, name)
		}
	}
	if c.Addr != ":9999" || c.DispatchInterval != Default().DispatchInterval {
		t.Errorf("after errors: %+v", c)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{"empty addr", func(c *Config) { c.Addr = "" }, "addr is empty"},
		{"zero interval", func(c *Config) { c.DispatchInterval = 0 }, "dispatch_interval must be positive"},
		{"negative interval", func(c *Config) { c.DispatchInterval = -time.Second }, "dispatch_interval must be positive"},
		{"zero stuck", func(c *Config) { c.StuckAfter = 0 }, "stuck_after must be positive"},
		{"unknown plan", func(c *Config) { c.DefaultPlan = "gold" }, `default_plan "gold"`},
		{"empty plan", func(c *Config) { c.DefaultPlan = "" }, `default_plan ""`},
		{"short secret", func(c *Config) { c.WebhookSecret = "hunter2" }, "webhook_secret is 7 bytes"},
		{"bad level", func(c *Config) { c.LogLevel = "trace" }, `log_level "trace"`},
	}
	for _, tt := range tests {
		c := Default()
		tt.change(&c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Validate() = %v, want an error mentioning %q", tt.name, err, tt.want)
		}
	}

	c := Default()
	c.WebhookSecret = strings.Repeat("x", MinSecretLen)
	if err := c.Validate(); err != nil {
		t.Errorf("secret of %d bytes: %v", MinSecretLen, err)
	}
}

func TestValidateJoins(t *testing.T) {
	c := Config{WebhookSecret: "short"}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate accepted an empty config")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Validate error %T is not a joined error", err)
	}
	if n := len(joined.Unwrap()); n != 6 {
		t.Errorf("Validate reported %d problems, want 6:\n%v", n, err)
	}
}
