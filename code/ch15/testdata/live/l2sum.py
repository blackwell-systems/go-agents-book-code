import json, glob, re, os
T='$TMP'
bad=["Library/Caches/go-build","go >= 1.27","bad record MAC","402"]
total=0
for i in range(1,11):
    S=f'{T}/l2-{i}'
    if not os.path.isdir(S): continue
    lives=sorted(glob.glob(S+'/life-*.txt'))
    texts=[open(f).read() for f in lives]
    resumed=[re.search(r'resuming run \S+: (\w+), (\d+) turns',t) for t in texts]
    final=texts[-1] if texts else ''
    grade=re.search(r'graded against the catalog: (\d+) of (\d+)',final)
    stopped=re.search(r'^stopped: (.*)$',final,re.M)
    try:
        st=json.load(open(f'{S}/l2-{i}.json')); cost=st['Usage']['Cost']; phase=st['Phase']; turns=st['Turns']
    except Exception as e:
        cost=0; phase='?'; turns=0
    total+=cost
    try:
        issues=json.load(open(f'{S}/l2-{i}.work/issues.json'))
    except Exception:
        issues=[]
    agent=[x for x in issues if x['id']>102]
    fps=[x.get('fingerprint','') for x in agent]
    dups=len(fps)-len(set(fps))
    comments={x['id']:len(x.get('comments') or []) for x in issues}
    flags=[b for b in bad if any(b in t for t in texts)]
    print(f"run {i}: lives {len(lives)}, resumed at {[ (m.group(1),m.group(2)) if m else None for m in resumed[1:]]}, "
          f"stopped {stopped.group(1) if stopped else None!r}, grade {grade.groups() if grade else None}, "
          f"issues filed {len(agent)} (dup fingerprints {dups}), comments {comments}, phase {phase}, turns {turns}, cost ${cost:.4f}, flags {flags}")
print(f"L2 total ${total:.4f}")
