#!/usr/bin/env python3
"""Publish the current CI-approved dev frontend after its backend is healthy.

Installed operational code runs on OMEN. Repository build code runs only inside
a disposable container, without cloud credentials, host home or Docker socket.
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import datetime
import fcntl
import gzip
import io
import json
import os
from pathlib import Path
import re
import shlex
import signal
import subprocess
import tarfile
import tempfile
import time
import uuid

from deploy import REPO, ci_passed, command, extract_release, fetch_revision, read_json
from install_frontend import (PART_BYTES, backend_ready, digest, read_files,
                              require, validate_manifest, verify_frontend)

BASE = Path.home() / '.local/share/human-worth-frontend'
INSTANCE = 'i-2zeb1p4opgtt6d5kb2hs'
REGION = 'cn-beijing'
ALIYUN = Path.home() / '.local/bin/aliyun'
INSTALLER = '/usr/local/lib/human-worth/install_frontend.py'
NODE_IMAGE = 'node:24.18.0-bookworm-slim@sha256:6f7b03f7c2c8e2e784dcf9295400527b9b1270fd37b7e9a7285cf83b6951452d'


def require_approved(sha):
    require(bool(re.fullmatch('[a-f0-9]{40}', sha)), 'Invalid release SHA')
    head = command(['git', 'ls-remote', 'https://github.com/' + REPO + '.git', 'refs/heads/dev'], text=True).split()[0]
    require(head == sha, 'Frontend release superseded by a newer dev revision')
    data = read_json('https://api.github.com/repos/' + REPO + '/actions/workflows/ci.yml/runs?branch=dev&event=push&head_sha=' + sha + '&per_page=100')
    require(ci_passed(data.get('workflow_runs', []), sha), 'Waiting for matching successful dev push CI')


def container(workspace, args, network):
    name = 'human-worth-frontend-build-' + uuid.uuid4().hex[:12]
    invocation = ['docker', 'run', '--rm', '--pull=never', '--name', name,
                  '--user', str(os.getuid()) + ':' + str(os.getgid()),
                  '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges',
                  '--pids-limit=128', '--memory=1g', '--cpus=2', '--network=' + network,
                  '--tmpfs', '/tmp:rw,nosuid,nodev,size=256m',
                  '--mount', 'type=bind,src=' + str(workspace) + ',dst=/work',
                  '--workdir', '/work/frontend', '--env', 'HOME=/tmp',
                  '--env', 'npm_config_cache=/tmp/npm', NODE_IMAGE, *args]
    try:
        subprocess.run(invocation, check=True, timeout=600)
    finally:
        # The exact name belongs to this invocation, including after a timeout.
        subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL, timeout=30)


def build(checkout, sha):
    release = BASE / 'builds' / sha
    if release.exists():
        files = read_files(release)
        manifest = json.loads(files['frontend-release.json'])
        require(manifest['revision'] == sha, 'Cached frontend revision mismatch')
        require({name: digest(data) for name, data in files.items() if name != 'frontend-release.json'}
                == validate_manifest(manifest), 'Cached frontend checksum mismatch')
        return manifest, files
    release.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='build-', dir=BASE) as folder:
        workspace = Path(folder)
        extract_release(command(['git', '--git-dir', str(checkout), 'archive', sha, 'frontend']), workspace)
        container(workspace, ['npm', 'ci', '--ignore-scripts', '--no-audit', '--no-fund'], 'bridge')
        # npm installation cannot execute package scripts; application code runs
        # only in this second, offline container with no inherited credentials.
        container(workspace, ['npm', 'run', 'build'], 'none')
        dist = workspace / 'frontend/dist'
        files = read_files(dist)
        require('frontend-release.json' not in files, 'Build must not supply its own revision marker')
        manifest = {'schema': 1, 'revision': sha,
                    'builtAt': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                    'files': {name: digest(data) for name, data in files.items()}}
        validate_manifest(manifest)
        files['frontend-release.json'] = (json.dumps(manifest, sort_keys=True, indent=2) + '\n').encode()
        (dist / 'frontend-release.json').write_bytes(files['frontend-release.json'])
        read_files(dist)
        dist.rename(release)
    return manifest, files


def cloud(operation, *args):
    try:
        result = subprocess.run([str(ALIYUN), 'ecs', operation, '--RegionId', REGION, *args],
                                capture_output=True, timeout=90, check=True)
        return json.loads(result.stdout)
    except (subprocess.SubprocessError, ValueError):
        # Do not put credential/configuration diagnostics or encoded payloads in journal.
        raise RuntimeError('Cloud Assistant request failed: ' + operation) from None


def wait_cloud(invoke, send=False, timeout=900):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = cloud('DescribeSendFileResults' if send else 'DescribeInvocationResults',
                       '--InvokeId', invoke, '--InstanceId', INSTANCE)
        if send:
            items = [item for entry in result.get('Invocations', {}).get('Invocation', [])
                     for item in entry.get('InvokeInstances', {}).get('InvokeInstance', [])]
        else:
            items = result.get('Invocation', {}).get('InvocationResults', {}).get('InvocationResult', [])
        for item in items:
            status = item.get('InvocationStatus')
            if status == 'Success':
                require(send or item.get('ExitCode') == 0, 'ECS command exited unsuccessfully')
                return base64.b64decode(item.get('Output', '')).decode(errors='replace')
            if status not in ['Pending', 'Running', 'Stopping', 'Scheduled', None]:
                raise RuntimeError('Cloud Assistant delivery failed: ' + str(status) + ' ' + str(item.get('ErrorCode', '')))
        time.sleep(2)
    raise TimeoutError('Cloud Assistant result deadline exceeded')


def remote(*args):
    content = base64.b64encode(shlex.join(['python3', INSTALLER, *args]).encode()).decode()
    result = cloud('RunCommand', '--InstanceId.1', INSTANCE, '--Type', 'RunShellScript',
                   '--ContentEncoding', 'Base64', '--CommandContent', content,
                   '--KeepCommand', 'false', '--Timeout', '900', '--Name', 'human-worth-frontend')
    output = wait_cloud(result['InvokeId'])
    return json.loads(output.strip().splitlines()[-1])


def send_file(name, data, target):
    encoded = base64.b64encode(data).decode()
    require(len(encoded) <= 32000, 'Cloud Assistant file exceeds Base64 limit')
    for attempt in range(3):
        try:
            result = cloud('SendFile', '--InstanceId.1', INSTANCE, '--Name', name,
                           '--TargetDir', target, '--ContentType', 'Base64', '--Content', encoded,
                           '--FileMode', '0600', '--Overwrite', 'true', '--Timeout', '60')
            wait_cloud(result['InvokeId'], send=True, timeout=90)
            return
        except (RuntimeError, TimeoutError):
            if attempt == 2:
                raise
            time.sleep(2)


def package(manifest, files, previous):
    require(previous.get('installer') == 1, 'Install the reviewed ECS frontend helper first')
    reused = {name: digest(data) for name, data in files.items()
              if previous['files'].get(name) == digest(data)}
    archive = io.BytesIO()
    with gzip.GzipFile(fileobj=archive, mode='wb', mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode='w') as tar:
            for name in sorted(set(files) - set(reused)):
                info = tarfile.TarInfo(name)
                info.size = len(files[name])
                info.mode = 0o644
                tar.addfile(info, io.BytesIO(files[name]))
    data = archive.getvalue()
    parameters = {'manifest': manifest, 'previous': previous['current'],
                  'config_sha256': previous['config_sha256'], 'reused_files': reused,
                  'archive_sha256': digest(data), 'archive_bytes': len(data),
                  'parts': (len(data) + PART_BYTES - 1) // PART_BYTES}
    return data, parameters


def save_state(state):
    target = BASE / 'state.json'
    temporary = target.with_suffix('.tmp')
    temporary.write_text(json.dumps(state, indent=2) + '\n')
    temporary.replace(target)


def reconcile(check_only=False):
    BASE.mkdir(mode=0o700, parents=True, exist_ok=True)
    checkout = BASE / 'repository.git'
    sha = fetch_revision(checkout)
    state_file = BASE / 'state.json'
    state = json.loads(state_file.read_text()) if state_file.exists() else {}
    if not check_only:
        if state.get('deployed_sha') == sha:
            print('Frontend already deployed ' + sha, flush=True)
            return
        if state.get('candidate_sha') == sha and time.time() < state.get('retry_after', 0):
            return
        state.update(candidate_sha=sha, retry_after=time.time() + 180)
        save_state(state)
    require_approved(sha)
    backend_ready(sha)
    manifest, files = build(checkout, sha)
    require_approved(sha)
    if check_only:
        print(json.dumps({'revision': sha, 'validated_build_files': len(files), 'activated': False}))
        return
    previous = remote('--inspect')
    if previous['files'] == {name: digest(data) for name, data in files.items()}:
        verify_frontend(sha, expected=manifest)
        backend_ready(sha)
        state.update(deployed_sha=sha, retry_after=0, deployment={'release': previous['current'], 'already_current': True})
        save_state(state)
        print('Verified existing frontend ' + sha, flush=True)
        return
    archive, parameters = package(manifest, files, previous)
    target = '/var/tmp/human-worth-frontend/' + parameters['archive_sha256']
    parts = [('part-%04d' % index, archive[offset:offset + PART_BYTES])
             for index, offset in enumerate(range(0, len(archive), PART_BYTES))]
    with ThreadPoolExecutor(max_workers=4) as pool:
        list(pool.map(lambda item: send_file(item[0], item[1], target), parts))
    send_file('parameters.json', json.dumps(parameters).encode(), target)
    # All transfer receipts must represent successful delivery, not acceptance.
    require_approved(sha)
    backend_ready(sha)
    result = remote(target)
    try:
        verify_frontend(sha, expected=manifest)
        backend_ready(sha)
    except Exception:
        remote('--rollback', result['backup'])
        raise
    state.update(deployed_sha=sha, retry_after=0, deployment=result,
                 deployed_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
    save_state(state)
    print('Frontend deployed ' + sha + '; public files and routes verified', flush=True)


if __name__ == '__main__':
    def terminate(signum, frame):
        raise InterruptedError('Frontend deployment interrupted')
    signal.signal(signal.SIGTERM, terminate)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check-only', action='store_true', help='Build approved dev in isolation without publishing')
    args = parser.parse_args()
    BASE.mkdir(mode=0o700, parents=True, exist_ok=True)
    with (BASE / 'deploy.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        reconcile(args.check_only)
