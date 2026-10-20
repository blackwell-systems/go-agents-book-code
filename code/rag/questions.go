package main

import "strings"

// question is one test question and what a right answer contains:
// every group must match, and a group matches when the answer holds
// any one of its phrases, ignoring case.
type question struct {
	Q    string
	Want [][]string
}

var questions = []question{
	{"What is the default rate limit per client?",
		[][]string{{"120"}}},
	{"Which release added link expiry?",
		[][]string{{"1.2.0"}}},
	{"How do I create a short link through the API?",
		[][]string{{"POST /api/links", "POST` /api/links", "/api/links"}}},
	{"Which environment variable overrides the listen address?",
		[][]string{{"LISTEN_ADDR"}}},
	{"What does a 429 response mean, and which header says when to retry?",
		[][]string{{"rate limit"}, {"Retry-After"}}},
	{"How long are daily click counts kept?",
		[][]string{{"400"}}},
	{"Every user hits the rate limit at once behind nginx. " +
		"What should I change?",
		[][]string{{"trusted_proxies"}}},
	{"A link I just created returns 404. Why might that be?",
		[][]string{{"expire"}}},
	{"What database does shortlink use, and what is the file " +
		"called by default?",
		[][]string{{"SQLite"}, {"shortlink.db"}}},
	{"How do I make shortlink's redirects permanent?",
		[][]string{{"redirect_status"}, {"301"}}},
	{"Why might a short link keep redirecting after I deleted it?",
		[][]string{{"301"}, {"cache"}}},
	{"How can a webhook receiver check that a request really came " +
		"from shortlink?",
		[][]string{{"X-Shortlink-Signature"}, {"HMAC"}}},
	{"I'm upgrading from v1.2.0 to v1.4.0. What do I need to do " +
		"about the database?",
		[][]string{{"shortlink migrate"}, {"backup", "back up"}}},
	{"What does a request get if its API key has the read scope " +
		"and it tries to create a link?",
		[][]string{{"403"}}},
	{"Which setting decides how soon a new click shows up in the " +
		"API, and what is its default?",
		[][]string{{"flush_interval"},
			{"5s", "5 s", "5 second", "five second"}}},
	{"Can shortlink make a QR code for a link?",
		[][]string{{"doesn't say", "does not say", "doesn't mention",
			"does not mention", "not mention", "no mention", "not covered",
			"don't mention", "not documented", "no information",
			"doesn't cover", "does not cover", "don't say", "do not say",
			"doesn't describe", "does not describe", "don't describe",
			"nothing in", "not addressed", "isn't addressed"}}},
}

// grade reports whether answer contains every group of q.Want.
func grade(q question, answer string) bool {
	a := strings.ToLower(answer)
	for _, group := range q.Want {
		ok := false
		for _, phrase := range group {
			if strings.Contains(a, strings.ToLower(phrase)) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
