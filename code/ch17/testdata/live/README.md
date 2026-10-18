Chapter 15's live runs (2026-10-17), on anthropic/claude-haiku-5.5
through OpenRouter, task m1-done-after-delete, each built from a clean
`git archive HEAD` copy of catalog and remind (`-repo`), with
`-defenses all -approve files -max-cost 0.15`. Paths are shortened to
$TMP (the scratch directory) and $BOOK (the repository).

- l1.sh: one run stopped with SIGTERM once its first issue_file call
  appeared, one killed with SIGKILL 0.3 s after it; each restarted with
  the same -run (l1-*-1.txt, l1-*-2.txt).
- l2.sh: ten runs killed with SIGKILL at a random time, 5 to 84 s in,
  then restarted with the same -run until they finished (l2-N-life-M.txt).
  l2sum.py prints l2-summary.txt from the runs' checkpoints and trackers.
- k1.py: the keyless run (`-keyless`): killed with SIGKILL by the
  command's `-die-after issue_file` hook right after the first filing
  returned, before its answer was saved (k1-life-1.txt). The checkpoint
  was copied three ways and restarted: with no person, `-approve files`
  (k1-wait.txt: the run waits); with a person at the terminal who
  answers "done" (k1-done.txt); and with one who answers "rerun"
  (k1-rerun.txt: the gate asks again, and the refile guard refuses).
- k1-question.txt: lines 7 to 12 of k1-done.txt, the question the
  person was asked, wrapped at 72 columns for the chapter, with the
  call's arguments cut short after their first words ("...").
