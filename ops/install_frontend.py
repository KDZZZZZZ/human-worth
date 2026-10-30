#!/usr/bin/env python3
"""Trusted ECS installer for static frontend releases; compatible with Python 3.6.

Install outside the checkout. Application archives never supply privileged code
or Nginx configuration. Only the current release symlink changes on activation.
"""
import argparse
import datetime
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import signal
import subprocess
import tarfile
import tempfile

ORIGIN = 'https://worth.oopsbox.cn'
BASE = Path('/var/www/human-worth-frontend')
BACKUPS = Path('/var/backups/human-worth')
CONFIG = Path('/etc/nginx/conf.d/human-worth-domain.conf')
STAGING = Path('/var/tmp/human-worth-frontend')
MAX_BYTES = 16 * 1024 * 1024
PART_BYTES = 22000
ROUTES = ['/', '/board', '/board.html', '/login', '/login.html',
          '/discovery', '/submit', '/submit.html', '/me/submissions',
          '/task-detail.html?id=deployment-check']
LOCAL = ['--resolve', 'worth.oopsbox.cn:443:127.0.0.1']


def digest(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def valid_name(name):
    path = PurePosixPath(name)
    return (bool(re.fullmatch(r'[A-Za-z0-9_./-]+', name))
            and str(path) == name and not path.is_absolute()
            and all(not part.startswith('.') for part in path.parts)
            and (name in ['index.html', 'frontend-release.json']
                 or path.parts[0] in ['assets', 'fonts', 'media']))


def validate_manifest(manifest):
    require(isinstance(manifest, dict) and manifest.get('schema') == 1,
            'Unsupported frontend manifest')
    require(bool(re.fullmatch(r'[a-f0-9]{40}', str(manifest.get('revision', '')))),
            'Invalid frontend revision')
    files = manifest.get('files')
    require(isinstance(files, dict) and 3 <= len(files) <= 255,
            'Invalid frontend file list')
    for name, sha in files.items():
        require(valid_name(name) and isinstance(sha, str)
                and bool(re.fullmatch(r'[a-f0-9]{64}', sha)), 'Invalid static file: ' + name)
    require('index.html' in files and 'frontend-release.json' not in files
            and any(name.startswith('assets/') and name.endswith('.js') for name in files)
            and any(name.startswith('assets/') and name.endswith('.css') for name in files),
            'Frontend entry or bundled assets missing')
    return files


def read_files(directory):
    files = {}
    total = 0
    for path in sorted(directory.rglob('*')):
        require(not path.is_symlink(), 'Static symlink refused')
        if path.is_dir():
            continue
        name = str(path.relative_to(directory))
        require(path.is_file() and valid_name(name), 'Unexpected static file: ' + name)
        total += path.stat().st_size
        require(total <= MAX_BYTES and len(files) < 256, 'Frontend exceeds release limit')
        files[name] = path.read_bytes()
    return files


def response(path, options=()):
    result = subprocess.run(['curl', '--silent', '--show-error', '--noproxy', '*',
                             '--connect-timeout', '5', '--max-time', '15',
                             '--header', 'Cache-Control: no-cache',
                             '--write-out', '\n%{http_code}', *options, ORIGIN + path],
                            check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=20)
    body, status = result.stdout.rsplit(b'\n', 1)
    return int(status), body


def backend_ready(revision, options=()):
    status, body = response('/api/health', options)
    health = json.loads(body.decode())
    require(status == 200 and health.get('status') == 'ok'
            and health.get('service') == 'human-worth'
            and health.get('revision') == revision
            and health.get('deploymentRevision') == revision,
            'Backend has not completed this revision')


def verify_frontend(revision, options=(), expected=None):
    status, body = response('/frontend-release.json', options)
    require(status == 200, 'Frontend release marker unavailable')
    manifest = json.loads(body.decode())
    files = validate_manifest(manifest)
    require(manifest['revision'] == revision, 'Frontend revision mismatch')
    if expected is not None:
        require(manifest == expected, 'Published frontend manifest differs from build')
    for name, sha in files.items():
        status, content = response('/' + name, options)
        require(status == 200 and digest(content) == sha, 'Published file mismatch: ' + name)
    for path in ROUTES:
        status, content = response(path, options)
        require(status == 200 and digest(content) == files['index.html'], 'SPA route failed: ' + path)
    for path in ['/assets/missing.js', '/.env', '/.local/dev.key', '/src/main.jsx', '/node_modules/']:
        require(response(path, options)[0] == 404, 'Private or missing path exposed: ' + path)
    return manifest


def frontend_healthy(revision):
    try:
        verify_frontend(revision)
        return True
    except (OSError, ValueError, subprocess.SubprocessError):
        return False


def current_release():
    link = BASE / 'current'
    require(link.is_symlink(), 'Initialize the existing static frontend before enabling automation')
    target = Path(os.readlink(str(link)))
    require(target.is_absolute() and not target.is_symlink()
            and target.resolve().parent == (BASE / 'releases').resolve(), 'Unexpected current release')
    return target


def inspect():
    current = current_release()
    files = read_files(current)
    return {'installer': 1, 'current': str(current),
            'files': {name: digest(content) for name, content in files.items()},
            'config_sha256': digest(CONFIG.read_bytes())}


def unpack(parameters, staging):
    manifest = parameters['manifest']
    expected = dict(validate_manifest(manifest))
    marker = (json.dumps(manifest, sort_keys=True, indent=2) + '\n').encode()
    expected['frontend-release.json'] = digest(marker)
    size, parts = parameters['archive_bytes'], parameters['parts']
    require(isinstance(size, int) and 0 < size <= MAX_BYTES
            and parts == (size + PART_BYTES - 1) // PART_BYTES, 'Invalid archive size')
    chunks = []
    for index in range(parts):
        path = staging / ('part-%04d' % index)
        require(not path.is_symlink() and path.stat().st_size <= PART_BYTES, 'Invalid archive part')
        chunks.append(path.read_bytes())
    archive = b''.join(chunks)
    require(len(archive) == size and digest(archive) == parameters['archive_sha256'], 'Archive checksum mismatch')
    reused = parameters.get('reused_files', {})
    require(isinstance(reused, dict) and set(reused) <= set(expected), 'Invalid reused file list')
    previous = current_release()
    require(str(previous) == parameters['previous'], 'Frontend changed since inspection')
    files = {}
    total = 0
    for name, sha in reused.items():
        path = previous / name
        require(not path.is_symlink() and path.resolve().is_file()
                and previous.resolve() in path.resolve().parents, 'Reused file escaped previous release')
        total += path.stat().st_size
        require(total <= MAX_BYTES, 'Frontend exceeds release limit')
        data = path.read_bytes()
        require(sha == expected[name] and digest(data) == sha, 'Reused file checksum mismatch: ' + name)
        files[name] = data
    with tarfile.open(fileobj=io.BytesIO(archive), mode='r:gz') as source:
        for member in source:
            require(member.name in expected and member.name not in files
                    and member.isfile(), 'Unexpected or repeated archive entry')
            total += member.size
            require(total <= MAX_BYTES, 'Frontend exceeds release limit')
            data = source.extractfile(member).read()
            require(digest(data) == expected[member.name], 'File checksum mismatch: ' + member.name)
            files[member.name] = data
    require(set(files) == set(expected), 'Incomplete frontend release')
    require(files['frontend-release.json'] == marker, 'Release marker differs from manifest')
    return files, previous


def activate_link(target):
    temp = BASE / 'current.next'
    if temp.is_symlink():
        temp.unlink()
    temp.symlink_to(target)
    temp.replace(BASE / 'current')


def rollback(backup):
    require(not backup.is_symlink() and backup.resolve().parent == BACKUPS.resolve()
            and bool(re.fullmatch(r'frontend-\d{8}T\d{6}Z-[a-f0-9]{12}', backup.name)), 'Invalid rollback record')
    record = json.loads((backup / 'deployment-result.json').read_text())
    previous = Path(record['previous'])
    require(previous.resolve().parent == (BASE / 'releases').resolve()
            and not previous.is_symlink(), 'Invalid previous release')
    require(str(current_release()) in [record['release'], record['previous']],
            'Refusing to roll back another deployment')
    activate_link(previous)
    status, body = response('/index.html', LOCAL)
    require(status == 200 and digest(body) == digest((previous / 'index.html').read_bytes()), 'Rollback health check failed')
    return {'restored': str(previous)}


def install(staging, check_only=False):
    parameter_file = staging / 'parameters.json'
    require(not parameter_file.is_symlink() and parameter_file.stat().st_size <= 128 * 1024,
            'Invalid deployment parameters')
    parameters = json.loads(parameter_file.read_text())
    files, previous = unpack(parameters, staging)
    manifest = parameters['manifest']
    revision = manifest['revision']
    require(digest(CONFIG.read_bytes()) == parameters['config_sha256'], 'Nginx configuration changed')
    backend_ready(revision, LOCAL)
    if check_only:
        return {'validated_files': len(files), 'revision': revision}
    subprocess.run(['nginx', '-t'], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    configs = {str(p): digest(p.read_bytes()) for p in CONFIG.parent.glob('*.conf')}
    release = BASE / 'releases' / (revision + '-' + parameters['archive_sha256'][:12])
    if release.exists():
        require(read_files(release) == files, 'Immutable release already exists with different files')
    else:
        building = Path(tempfile.mkdtemp(prefix='.building-', dir=str(BASE / 'releases')))
        try:
            for name, data in files.items():
                path = building / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data)
                path.chmod(0o644)
            for path in [building] + [p for p in building.rglob('*') if p.is_dir()]:
                path.chmod(0o755)
            building.rename(release)
        finally:
            if building.exists():
                shutil.rmtree(str(building))
    stamp = datetime.datetime.utcnow().strftime('%Y%m%dT%H%M%SZ')
    backup = BACKUPS / ('frontend-' + stamp + '-' + parameters['archive_sha256'][:12])
    backup.mkdir(mode=0o700, parents=True, exist_ok=True)
    (backup / 'previous-link.json').write_text(json.dumps(str(previous)))
    require(current_release() == previous, 'Frontend changed before activation')
    result = {'revision': revision, 'release': str(release), 'previous': str(previous),
              'backup': str(backup), 'files': len(files)}
    try:
        activate_link(release)
        verify_frontend(revision, LOCAL, manifest)
        backend_ready(revision, LOCAL)
        require(all(digest(Path(p).read_bytes()) == sha for p, sha in configs.items()), 'Nginx configuration changed during deployment')
        (backup / 'deployment-result.json').write_text(json.dumps(result, indent=2) + '\n')
    except BaseException:
        activate_link(previous)
        status, body = response('/index.html', LOCAL)
        require(status == 200 and digest(body) == digest((previous / 'index.html').read_bytes()),
                'Frontend activation and rollback checks both failed')
        raise
    return result


if __name__ == '__main__':
    def terminate(signum, frame):
        raise InterruptedError('Frontend installation interrupted')
    signal.signal(signal.SIGTERM, terminate)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('staging', nargs='?')
    parser.add_argument('--inspect', action='store_true')
    parser.add_argument('--check-only', action='store_true')
    parser.add_argument('--rollback', help='Restore an installer backup only while its release is current')
    args = parser.parse_args()
    require(os.geteuid() == 0, 'Run the installed ECS helper as root')
    with (BASE / 'deploy.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.rollback:
            result = rollback(Path(args.rollback))
        elif args.inspect:
            result = inspect()
        else:
            staging = Path(args.staging or '')
            require(staging.parent == STAGING and bool(re.fullmatch('[a-f0-9]{64}', staging.name)), 'Invalid staging directory')
            result = install(staging, args.check_only)
        print(json.dumps(result))
