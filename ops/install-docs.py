#!/usr/bin/env python3
"""Install a verified Swagger artifact on the existing ECS Nginx gateway."""
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import time
from urllib.error import URLError
from urllib.request import urlopen


def sha256(content):
    return hashlib.sha256(content).hexdigest()


def unlink(path):
    try:
        path.unlink()
    except FileNotFoundError:
        pass


def install(archive_path, expected_sha):
    if os.geteuid() != 0:
        raise PermissionError('Run on the gateway as root')
    archive = Path(archive_path).read_bytes()
    if sha256(archive) != expected_sha:
        raise ValueError('Artifact checksum mismatch')
    with tarfile.open(fileobj=io.BytesIO(archive), mode='r:gz') as source:
        names = ['index.html', 'swagger-init.js', 'docs.css', 'openapi.yaml', 'manifest.json', 'nginx.conf', 'vendor.json']
        if sorted(source.getnames()) != sorted(names) or not all(item.isfile() for item in source):
            raise ValueError('Unexpected artifact contents')
        files = {name: source.extractfile(name).read() for name in names}
    manifest = json.loads(files['manifest.json'])
    vendor = json.loads(files.pop('vendor.json'))
    version = manifest['swaggerUiVersion']
    url = f'https://registry.npmjs.org/swagger-ui-dist/-/swagger-ui-dist-{version}.tgz'
    if vendor['url'] != url:
        raise ValueError('Unexpected Swagger distribution URL')
    with urlopen(url, timeout=60) as response:
        package = response.read()
    integrity = 'sha512-' + base64.b64encode(hashlib.sha512(package).digest()).decode()
    if integrity != vendor['integrity']:
        raise ValueError('Swagger distribution integrity mismatch')
    vendor_names = ['swagger-ui-bundle.js', 'swagger-ui.css', 'favicon-32x32.png', 'LICENSE', 'NOTICE', 'swagger-ui-bundle.js.LICENSE.txt']
    with tarfile.open(fileobj=io.BytesIO(package), mode='r:gz') as source:
        for name in vendor_names:
            files[name] = source.extractfile('package/' + name).read()
    config = files.pop('nginx.conf')
    if set(files) != set(manifest['files']) | {'manifest.json'}:
        raise ValueError('Manifest file list mismatch')
    for name, expected in manifest['files'].items():
        if Path(name).name != name or sha256(files[name]) != expected:
            raise ValueError(f'File checksum mismatch: {name}')

    configuration = Path('/etc/nginx/conf.d/human-worth.conf')
    original = configuration.read_bytes()
    if sha256(original) not in [vendor['previousConfigSha256'], sha256(config)]:
        raise ValueError('Gateway config changed since inspection; preserve it and inspect again')
    base = Path('/var/www/human-worth-docs')
    release = base / 'releases' / expected_sha
    release.mkdir(parents=True, exist_ok=True)
    for directory in [base, base / 'releases', release]:
        directory.chmod(0o755)
    for name, content in files.items():
        (release / name).write_bytes(content)
        (release / name).chmod(0o644)
    backups = base / 'backups'
    backups.mkdir(mode=0o700, exist_ok=True)
    backup = backups / f'{expected_sha}.conf'
    if not backup.exists():
        backup.write_bytes(original)
        backup.chmod(0o600)
    current = base / 'current'
    previous = Path(os.readlink(str(current))) if current.is_symlink() else None
    health_url = 'http://127.0.0.1:18090/api/health'
    before = json.load(urlopen(health_url, timeout=15))
    try:
        configuration.write_bytes(config)
        subprocess.run(['nginx', '-t'], check=True)
        next_link = base / 'current.next'
        unlink(next_link)
        next_link.symlink_to(release)
        next_link.replace(current)
        subprocess.run(['systemctl', 'reload', 'nginx'], check=True)
        for attempt in range(20):
            try:
                with urlopen('http://127.0.0.1:18090/docs/openapi.yaml', timeout=15) as response:
                    if sha256(response.read()) != manifest['files']['openapi.yaml']:
                        raise RuntimeError('Published OpenAPI differs from the validated artifact')
                break
            except (URLError, RuntimeError):
                if attempt == 19:
                    raise
                time.sleep(0.25)
        if json.load(urlopen(health_url, timeout=15)) != before:
            raise RuntimeError('Application health changed during docs publication')
    except Exception:
        configuration.write_bytes(original)
        if previous is None:
            unlink(current)
        else:
            next_link = base / 'rollback.next'
            unlink(next_link)
            next_link.symlink_to(previous)
            next_link.replace(current)
        subprocess.run(['nginx', '-t'], check=True)
        subprocess.run(['systemctl', 'reload', 'nginx'], check=True)
        raise
    print(json.dumps({'artifact': expected_sha, 'url': 'http://123.56.161.234:18090/docs/', 'swaggerUiVersion': version, 'applicationRevision': before['revision']}))


if __name__ == '__main__':
    install(*sys.argv[1:])
