package main

import (
	"encoding/json/jsontext"

	"github.com/blackwell-systems/go-agents-book/code/ch11/agent"
)

// distractors are plausible tools the triage agent doesn't have, added
// to measure what a long tool list costs. Some sound like the real
// ones (crash_view, code_find, test_run), as tools from several
// sources often do.
var distractors = []struct{ name, desc string }{
	{"crash_view", "View a crash."},
	{"crash_export", "Export crash reports to a file."},
	{"crash_stats", "Count crashes per day."},
	{"code_find", "Find code."},
	{"code_format", "Format a Go file with gofmt."},
	{"code_blame", "Show who last changed each line of a file."},
	{"test_run", "Run a test."},
	{"test_coverage", "Report test coverage for a package."},
	{"issue_lookup", "Look up an issue."},
	{"issue_close", "Close an issue."},
	{"issue_label", "Add a label to an issue."},
	{"log_query", "Query the service's logs."},
	{"log_tail", "Show the newest log lines."},
	{"metrics_query", "Query the service's metrics."},
	{"deploy_status", "Show what version is deployed where."},
	{"deploy_rollback", "Roll a deployment back."},
	{"db_query", "Run a read-only SQL query."},
	{"http_get", "Fetch a URL."},
	{"web_search", "Search the web."},
	{"doc_search", "Search the team's documentation."},
	{"slack_post", "Post a message to a Slack channel."},
	{"email_send", "Send an email."},
	{"calendar_create", "Create a calendar event."},
	{"pager_ack", "Acknowledge a page."},
	{"user_lookup", "Look up a user of the service."},
	{"feature_flag", "Turn a feature flag on or off."},
	{"config_get", "Read a configuration value."},
	{"secret_get", "Read a secret."},
	{"file_upload", "Upload a file."},
	{"translate", "Translate text."},
}

func distractorDefs() []agent.ToolDef {
	var defs []agent.ToolDef
	for _, d := range distractors {
		defs = append(defs, agent.ToolDef{Name: d.name, Description: d.desc,
			Parameters: jsontext.Value(`{"type": "object"}`)})
	}
	return defs
}
