#!/bin/zsh
# L1: kill and resume, two ways, on m1 with the live model.
T=$TMP
BIN=$T/triage15
REPO=$T/clean/code
export OPENROUTER_API_KEY=$(security find-generic-password -s openrouter -w)
export PATH=$(cd $BOOK/code && go env GOROOT)/bin:$PATH
export GOCACHE=$T/gocache
run() { # $1 state, $2 run id, $3 out file
  $BIN -repo $REPO -task m1-done-after-delete -state $1 -run $2 \
    -defenses all -approve files -max-cost 0.15 > $3 2>&1 &
  echo $!
}
waitfor() { # $1 file, $2 pattern
  for i in {1..600}; do grep -q "$2" $1 && return 0; sleep 0.5; done; return 1
}
for mode in term kill; do
  S=$T/l1-$mode; rm -rf $S; mkdir -p $S
  pid=$(run $S l1-$mode $T/l1-$mode-1.txt)
  waitfor $T/l1-$mode-1.txt "issue_file" || echo "no filing seen ($mode)"
  if [ $mode = term ]; then kill -TERM $pid; else sleep 0.3; kill -KILL $pid; fi
  wait $pid 2>/dev/null
  pid=$(run $S l1-$mode $T/l1-$mode-2.txt); wait $pid
  echo "== $mode: issues in the tracker"
  python3 -c "import json;d=json.load(open('$S/l1-$mode.work/issues.json'));print([(i['id'],i.get('fingerprint',''),i['title'][:40]) for i in d])"
done
