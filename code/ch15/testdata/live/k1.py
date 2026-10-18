"""Chapter 15's keyless live run: kill right after the first issue_file
call returns, then restart three ways: with no person (the run waits),
with a person who answers "done" (the truth), and with one who answers
"rerun" (a mistake the refile guard catches)."""
import os, re, shutil, subprocess, sys, json
T = '$TMP'
BIN = T + '/triage15'
REPO = T + '/clean/code'
env = dict(os.environ)
env['OPENROUTER_API_KEY'] = subprocess.check_output(
    ['security', 'find-generic-password', '-s', 'openrouter', '-w']).decode().strip()
env['GOCACHE'] = T + '/gocache'
goroot = subprocess.check_output(['go', 'env', 'GOROOT'], cwd='$BOOK/code').decode().strip()
env['PATH'] = goroot + '/bin:' + env['PATH']

def cmd(state, approve, extra=()):
    return [BIN, '-repo', REPO, '-task', 'm1-done-after-delete', '-state', state,
            '-run', 'k1', '-keyless', '-defenses', 'all', '-approve', approve,
            '-max-cost', '0.15', *extra]

def run(state, approve, out, answer=None, extra=()):
    """Run the command; answer each prompt: approvals with yes, the
    outcome question with answer(code)."""
    p = subprocess.Popen(cmd(state, approve, extra), env=env, stdin=subprocess.PIPE,
                         stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    buf = ''
    with open(out, 'w') as f:
        while True:
            ch = p.stdout.read(1)
            if not ch:
                break
            c = ch.decode('utf-8', 'replace')
            f.write(c); f.flush()
            buf += c
            m = re.search(r'type "yes (\w+)" to approve: $', buf)
            if m:
                p.stdin.write(f'yes {m.group(1)}\n'.encode()); p.stdin.flush(); buf = ''
                continue
            m = re.search(r'type "rerun (\w+)" if it didn\'t, or "done \w+ what you found" if it did: $', buf)
            if m:
                line = answer(m.group(1))
                f.write(f'[the person types: {line}]\n')
                p.stdin.write((line + '\n').encode()); p.stdin.flush(); buf = ''
    return p.wait()

S = T + '/k1'
shutil.rmtree(S, ignore_errors=True); os.makedirs(S)
rc = run(S, 'files', T + '/k1-life-1.txt', extra=('-die-after', 'issue_file'))
print('life 1 exit', rc)
for name in ['k1-wait', 'k1-done', 'k1-rerun']:
    shutil.rmtree(T + '/' + name, ignore_errors=True)
    shutil.copytree(S, T + '/' + name)
rc = run(T + '/k1-wait', 'files', T + '/k1-wait.txt')
print('no person: exit', rc)
rc = run(T + '/k1-done', 'ask', T + '/k1-done.txt', answer=lambda c: f'done {c} it is filed as #103')
print('done: exit', rc)
rc = run(T + '/k1-rerun', 'ask', T + '/k1-rerun.txt', answer=lambda c: f'rerun {c}')
print('rerun: exit', rc)
for name in ['k1', 'k1-done', 'k1-rerun']:
    d = json.load(open(f'{T}/{name}/k1.work/issues.json'))
    print(name, [(i['id'], i.get('fingerprint', ''), len(i.get('comments') or [])) for i in d])
