#!/usr/bin/env python3
"""Pull only a successful, current dev revision; atomically activate or roll back.

Install this controller outside the checkout. It never executes repository scripts.
The controller account may restart only human-worth.service through sudo.
"""
import fcntl
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
import time
from urllib.request import Request, urlopen

REPO = 'KDZZZZZZ/human-worth'
BASE = Path('/opt/human-worth')


def ci_passed(runs, sha):
    candidates = [run for run in runs if (
        run.get('head_sha') == sha and run.get('head_branch') == 'dev'
        and run.get('event') == 'push'
        and run.get('path') == '.github/workflows/ci.yml'
        and run.get('head_repository', {}).get('full_name') == REPO
    )]
    if not candidates:
        return False
    latest = max(candidates, key=lambda run: (run['id'], run.get('run_attempt', 1)))
    return latest.get('status') == 'completed' and latest.get('conclusion') == 'success'


def command(args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, timeout=90, **kwargs).stdout


def read_json(url):
    request = Request(url, headers={'Accept': 'application/vnd.github+json', 'User-Agent': 'human-worth-deployer'})
    with urlopen(request, timeout=20) as response:
        return json.load(response)


def save_state(state):
    path = BASE / 'state.json'
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(state, indent=2) + '\n')
    temp.replace(path)


def activate(release):
    link = BASE / 'current.next'
    link.unlink(missing_ok=True)
    link.symlink_to(release, target_is_directory=True)
    link.replace(BASE / 'current')
    command(['sudo', '-n', '/usr/bin/systemctl', 'restart', 'human-worth.service'])


def healthy(sha):
    for _ in range(20):
        try:
            data = read_json('http://127.0.0.1:18090/api/health')
            if data.get('status') == 'ok' and data.get('revision') == sha:
                return True
        except (OSError, ValueError):
            pass
        time.sleep(1)
    return False


def extract_release(archive, release):
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        for member in tar.getmembers():
            name = PurePosixPath(member.name)
            if name.is_absolute() or '..' in name.parts or not (member.isfile() or member.isdir()):
                raise ValueError(f'Unsupported archive entry: {member.name}')
            target = release / str(name)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(tar.extractfile(member).read())
                target.chmod(0o644)


def reconcile():
    checkout = BASE / 'repository.git'
    if not checkout.exists():
        command(['git', 'init', '--bare', str(checkout)])
    git = ['git', '--git-dir', str(checkout)]
    command(git + ['fetch', '--quiet', '--depth=1', f'https://github.com/{REPO}.git', 'refs/heads/dev:refs/heads/dev'])
    sha = command(git + ['rev-parse', 'refs/heads/dev'], text=True).strip()
    if not re.fullmatch(r'[a-f0-9]{40}', sha):
        raise ValueError('Unexpected revision')
    state_file = BASE / 'state.json'
    state = json.loads(state_file.read_text()) if state_file.exists() else {}
    if state.get('deployed_sha') == sha:
        print(f'Already deployed {sha}', flush=True)
        return
    if state.get('candidate_sha') == sha and time.time() < state.get('retry_after', 0):
        return
    state.update(candidate_sha=sha, retry_after=time.time() + 180)
    save_state(state)
    result = read_json(f'https://api.github.com/repos/{REPO}/actions/workflows/ci.yml/runs?branch=dev&event=push&head_sha={sha}&per_page=100')
    if not ci_passed(result.get('workflow_runs', []), sha):
        print(f'Waiting for successful push CI: {sha}', flush=True)
        return
    # A newer merge during the API check is handled on the next tick, without activating stale work.
    current_head = command(['git', 'ls-remote', f'https://github.com/{REPO}.git', 'refs/heads/dev'], text=True).split()[0]
    if current_head != sha:
        print(f'Superseded before activation: {sha}', flush=True)
        return
    release = BASE / 'releases' / sha
    if not release.exists():
        staging = BASE / 'releases' / f'{sha}.staging-{os.getpid()}'
        staging.mkdir(parents=True)
        extract_release(command(git + ['archive', sha]), staging)
        (staging / 'REVISION').write_text(sha + '\n')
        for required in ['app/server.mjs', 'public/index.html', 'public/style.css']:
            if not (staging / required).is_file():
                raise ValueError(f'Release missing {required}')
        staging.rename(release)
    previous = (BASE / 'current').resolve() if (BASE / 'current').is_symlink() else None
    try:
        activate(release)
        if not healthy(sha):
            raise RuntimeError('New release health check failed')
    except Exception:
        if previous:
            activate(previous)
            old_sha = (previous / 'REVISION').read_text().strip()
            if not healthy(old_sha):
                raise RuntimeError('Deployment and rollback health checks both failed')
            print(f'Rolled back to {old_sha}', flush=True)
        else:
            command(['sudo', '-n', '/usr/bin/systemctl', 'stop', 'human-worth.service'])
            (BASE / 'current').unlink(missing_ok=True)
        raise
    state.update(deployed_sha=sha, deployed_at=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), retry_after=0)
    save_state(state)
    print(f'Deployed {sha}; health check passed', flush=True)


if __name__ == '__main__':
    with (BASE / 'deploy.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        reconcile()
