#!/bin/zsh
# L2: ten runs killed with SIGKILL at a random time, then restarted with
# the same -run until they finish (at most four starts after the kill).
T=$TMP
BIN=$T/triage15
REPO=$T/clean/code
export OPENROUTER_API_KEY=$(security find-generic-password -s openrouter -w)
export PATH=$(cd $BOOK/code && go env GOROOT)/bin:$PATH
export GOCACHE=$T/gocache
# What the runs cost so far, from their checkpoints (which count every
# life's model calls), plus the L1 runs and the one new run started by
# mistake in L1.
spent() { python3 -c "
import json,glob
t=0.0
for f in glob.glob('$T/l1-*/*.json')+glob.glob('$T/l2-*/l2-*.json'):
    try: t+=json.load(open(f)).get('Usage',{}).get('Cost',0)
    except Exception: pass
print(round(t,4))"; }
for i in {1..10}; do
  S=$T/l2-$i; rm -rf $S; mkdir -p $S
  if (( $(echo "$(spent) > 0.80" | bc) )); then echo "budget reached"; break; fi
  delay=$(( RANDOM % 80 + 5 ))
  $BIN -repo $REPO -task m1-done-after-delete -state $S -run l2-$i \
    -defenses all -approve files -max-cost 0.15 > $S/life-1.txt 2>&1 &
  pid=$!
  sleep $delay
  kill -KILL $pid 2>/dev/null && echo "run $i: killed at ${delay}s" || echo "run $i: finished before ${delay}s"
  wait $pid 2>/dev/null
  pkill -KILL -f "serve-tracker $S/" 2>/dev/null
  for life in 2 3 4 5; do
    grep -q "^stopped: the model answered" $S/life-$((life-1)).txt && break
    $BIN -repo $REPO -task m1-done-after-delete -state $S -run l2-$i \
      -defenses all -approve files -max-cost 0.15 > $S/life-$life.txt 2>&1
  done
done
echo "spent $(spent)"
