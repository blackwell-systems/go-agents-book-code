Records for chapter 15's Try it notes in the exercises appendix
(2026-10-17), all offline, with no API key. Paths are shortened to $TMP
(the scratch directory) and $BOOK (the repository).

- try1-life-1.txt: `go run ./ch15 -task m1-done-after-delete -state
  $TMP/try1 -approve files -die-after code_patch`, killed with SIGKILL
  after the first code_patch returned.
- try1-checkpoint.txt: the fields of the checkpoint it left, printed
  with a short Python script (Phase, Turns, Resumes, Attempts, and the
  names of the last reply's calls).
- try1-life-2.txt: the same command without -die-after, which resumed
  the run and finished it.
- try3-keys-off.txt: the crash sweep's totals with keys turned off in
  the last variant.
