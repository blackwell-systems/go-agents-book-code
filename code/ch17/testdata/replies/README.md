Saved runs of the triage agent, one script per catalog task, recorded
with go run ./ch08 -record DIR -task ID and copied here.

The group-fp_*.json files are chapter 17's offline demo: one Haiku run
of the command on the whole catalog, recorded on 2026-10-18 with
go run ./ch17 -task batch -parallel 1 -approve files -max-cost 0.75
-record DIR catalog, one script per fingerprint group. Each reply is
kept as one streamed chunk with its finish and usage, without the
encrypted reasoning, and each request keeps only the model and the
assistant messages, which is all the replaying transport reads. Replay
them with the same flags: one group at a time, the groups file their
issues in the recorded order.
