package main

// tag::cases[]

// A testCase is one request and the tools that answer it: the best
// tool scores 1, an acceptable one 0.5, anything else 0. A case whose
// best tool is "" wants an answer in text, with no tool at all.
type testCase struct {
	ask  string
	best string
	ok   []string // acceptable alternatives
}

var cases = []testCase{
	{"Which crashes have we had?", "crash_list", nil},
	{"Show me the stack trace of report cr_1e2ea0.", "crash_read", nil},
	{"Did any crash happen on the snooze endpoint?", "crash_search",
		[]string{"crash_list"}},
	{"What did remind log just before cr_ee551c crashed?", "crash_logs",
		[]string{"crash_read"}},
	{"Report cr_85335e is a stuck request. What were its goroutines " +
		"doing?", "crash_goroutines", []string{"crash_read"}},
	{"Where is parseBearer defined?", "code_search", nil},
	{"Show me lines 120 to 160 of api/reminders.go.", "code_read", nil},
	{"What files does the store package have?", "code_list",
		[]string{"code_search"}},
	{"Do the store package's tests pass right now?", "run_tests", nil},
	{"In store/store.go, which you've read, change \"\\t\\tr = nil\" to " +
		"\"\\t\\tr, err = nil, ErrNotFound\".", "code_patch",
		[]string{"code_read"}},
	{"Is the dispatcher crash already in the tracker?", "issue_search",
		[]string{"crash_list"}},
	{"File an issue titled \"store.Get returns deleted reminders\" for " +
		"fingerprint fp_4828a6, saying Get must return ErrNotFound.",
		"issue_file", []string{"issue_search"}},
	{"Add a comment to issue 101 that it happened three more times.",
		"issue_comment", []string{"issue_search"}},
	{"You've fixed and filed everything. Hand in your diagnosis: group " +
		"fp_4828a6, reports cr_1e2ea0 and cr_ee551c, fixed, issue 103.",
		"submit_diagnosis", []string{"issue_search"}},
	{"In one sentence, what does idempotent mean?", "", nil},
}

// end::cases[]
