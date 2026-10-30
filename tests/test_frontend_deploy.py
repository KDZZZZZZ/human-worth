import contextlib
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import patch
from urllib.parse import urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'ops'))
import frontend_release as release
import install_frontend as installer
import verify_deployment

OLD, NEW = 'a' * 40, 'b' * 40


def built_files(revision):
    files = {'index.html': ('<html>' + revision + '</html>').encode(),
             'assets/main.js': ('console.log("' + revision + '")').encode(),
             'assets/main.css': b'body { color: black }'}
    manifest = {'schema': 1, 'revision': revision, 'builtAt': '2026-10-30T00:00:00Z',
                'files': {name: installer.digest(data) for name, data in files.items()}}
    files['frontend-release.json'] = (json.dumps(manifest, sort_keys=True, indent=2) + '\n').encode()
    return manifest, files


class FrontendDeploymentTest(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.base = self.root / 'public'
        self.old = self.base / 'releases' / (OLD + '-' + 'c' * 12)
        self.old.mkdir(parents=True)
        for name, data in built_files(OLD)[1].items():
            path = self.old / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        (self.base / 'current').symlink_to(self.old)
        config = self.root / 'nginx/domain.conf'
        config.parent.mkdir()
        config.write_text('existing static root and private TLS proxy\n')
        (config.parent / 'neighbor.conf').write_text('unrelated site\n')
        for name, value in [('BASE', self.base), ('BACKUPS', self.root / 'backups'), ('CONFIG', config)]:
            self.stack.enter_context(patch.object(installer, name, value))
        self.stack.enter_context(patch.object(release, 'BASE', self.root / 'controller'))
        self.backend = NEW
        self.corrupt_asset = False
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                path = urlsplit(self.path).path
                if path == '/api/health':
                    data = json.dumps({'service': 'human-worth', 'status': 'ok',
                                       'revision': fixture.backend, 'deploymentRevision': fixture.backend}).encode()
                elif path in [urlsplit(route).path for route in installer.ROUTES]:
                    data = (fixture.base / 'current/index.html').read_bytes()
                else:
                    target = fixture.base / 'current' / path.lstrip('/')
                    if not target.is_file():
                        self.send_response(404)
                        self.end_headers()
                        return
                    data = target.read_bytes()
                    if fixture.corrupt_asset and path.endswith('.js'):
                        data = b'wrong asset returned by gateway'
                self.send_response(200)
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop_server)
        self.stack.enter_context(patch.object(installer, 'response', self.http_response))
        self.stack.enter_context(patch.object(installer.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0)))

    def stop_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def http_response(self, path, options=()):
        connection = http.client.HTTPConnection(*self.server.server_address, timeout=3)
        try:
            connection.request('GET', path)
            reply = connection.getresponse()
            return reply.status, reply.read()
        finally:
            connection.close()

    def staged(self):
        manifest, files = built_files(NEW)
        archive, parameters = release.package(manifest, files, installer.inspect())
        staging = self.root / 'staging'
        staging.mkdir(exist_ok=True)
        self.write_archive(staging, archive, parameters)
        return staging, parameters

    def write_archive(self, staging, archive, parameters):
        parameters.update(archive_sha256=installer.digest(archive), archive_bytes=len(archive),
                          parts=(len(archive) + installer.PART_BYTES - 1) // installer.PART_BYTES)
        for index, offset in enumerate(range(0, len(archive), installer.PART_BYTES)):
            (staging / ('part-%04d' % index)).write_bytes(archive[offset:offset + installer.PART_BYTES])
        (staging / 'parameters.json').write_text(json.dumps(parameters))

    def test_real_http_activation_reuses_verified_files_and_preserves_previous_and_nginx(self):
        staging, parameters = self.staged()
        self.assertIn('assets/main.css', parameters['reused_files'])
        before = {p.name: p.read_bytes() for p in installer.CONFIG.parent.iterdir()}
        preview = installer.install(staging, check_only=True)
        self.assertEqual(preview['validated_files'], 4)
        self.assertEqual(installer.current_release(), self.old)
        result = installer.install(staging)
        self.assertEqual(installer.current_release(), Path(result['release']))
        self.assertTrue(installer.frontend_healthy(NEW))
        self.assertFalse(installer.frontend_healthy(OLD))
        self.assertEqual(self.old.joinpath('index.html').read_bytes(), built_files(OLD)[1]['index.html'])
        self.assertEqual(before, {p.name: p.read_bytes() for p in installer.CONFIG.parent.iterdir()})
        self.assertEqual(json.loads((Path(result['backup']) / 'previous-link.json').read_text()), str(self.old))
        installer.rollback(Path(result['backup']))
        self.assertEqual(installer.current_release(), self.old)
        self.assertEqual(self.http_response('/')[1], built_files(OLD)[1]['index.html'])

    def test_real_http_asset_failure_rolls_back_before_reporting_success(self):
        staging, _ = self.staged()
        self.corrupt_asset = True
        with self.assertRaisesRegex(ValueError, 'Published file mismatch'):
            installer.install(staging)
        self.assertEqual(installer.current_release(), self.old)
        self.assertEqual(self.http_response('/')[1], built_files(OLD)[1]['index.html'])
        self.assertEqual(list((self.root / 'backups').rglob('deployment-result.json')), [])

    def test_unwritable_completion_record_restores_previous_release(self):
        staging, _ = self.staged()
        original = Path.write_text

        def write(path, *args, **kwargs):
            if path.name == 'deployment-result.json':
                raise OSError('disk full')
            return original(path, *args, **kwargs)

        with patch.object(Path, 'write_text', write), self.assertRaisesRegex(OSError, 'disk full'):
            installer.install(staging)
        self.assertEqual(installer.current_release(), self.old)
        self.assertEqual(self.http_response('/')[1], built_files(OLD)[1]['index.html'])

    def test_corrupted_archive_reused_file_backend_and_config_drift_never_activate(self):
        for failure in ['archive', 'reused', 'backend', 'config', 'previous']:
            with self.subTest(failure=failure):
                staging, parameters = self.staged()
                css = self.old / 'assets/main.css'
                original_css, original_config = css.read_bytes(), installer.CONFIG.read_bytes()
                if failure == 'archive':
                    (staging / 'part-0000').write_bytes(b'corrupted')
                elif failure == 'reused':
                    css.write_bytes(b'corrupted')
                elif failure == 'backend':
                    self.backend = OLD
                elif failure == 'config':
                    installer.CONFIG.write_text('changed by another operator')
                else:
                    parameters['previous'] = '/unrelated/release'
                    (staging / 'parameters.json').write_text(json.dumps(parameters))
                with self.assertRaises(ValueError):
                    installer.install(staging)
                self.assertEqual(installer.current_release(), self.old)
                self.backend = NEW
                css.write_bytes(original_css)
                installer.CONFIG.write_bytes(original_config)

    def test_archive_rejects_traversal_symlinks_duplicates_and_missing_files(self):
        for name, kind in [('../escape', tarfile.REGTYPE), ('/escape', tarfile.REGTYPE),
                           ('index.html', tarfile.SYMTYPE), ('assets/main.css', tarfile.REGTYPE)]:
            staging, parameters = self.staged()
            data = io.BytesIO()
            with tarfile.open(fileobj=data, mode='w:gz') as source:
                member = tarfile.TarInfo(name)
                member.type = kind
                member.linkname = '/etc/passwd'
                source.addfile(member)
            self.write_archive(staging, data.getvalue(), parameters)
            with self.subTest(name=name), self.assertRaises(ValueError):
                installer.install(staging)
            self.assertEqual(installer.current_release(), self.old)
        staging, parameters = self.staged()
        parameters['parts'] += 1
        (staging / 'parameters.json').write_text(json.dumps(parameters))
        with self.assertRaisesRegex(ValueError, 'archive size'):
            installer.install(staging)

    def test_static_symlink_and_source_paths_are_rejected(self):
        (self.old / 'assets/private.key').symlink_to(installer.CONFIG)
        with self.assertRaisesRegex(ValueError, 'symlink'):
            installer.inspect()
        manifest = built_files(NEW)[0]
        for name in ['../private', '/private', '.env', 'src/main.js', 'assets/../secret', 'assets/.env']:
            malformed = dict(manifest, files={**manifest['files'], name: 'a' * 64})
            with self.subTest(name=name), self.assertRaises(ValueError):
                installer.validate_manifest(malformed)

    def test_backend_success_alone_cannot_finish_deploy_action(self):
        with patch.object(verify_deployment, 'require_current_ci'), patch.object(verify_deployment, 'healthy', return_value=True), patch.object(verify_deployment, 'frontend_healthy', return_value=False):
            with self.assertRaisesRegex(RuntimeError, 'frontend'):
                verify_deployment.wait_for_deployment(NEW, [], timeout=0)

    def test_ci_backend_and_supersession_gates_block_cloud_writes(self):
        for gate in ['ci', 'backend', 'superseded']:
            approvals = [ValueError('CI rejected')] if gate == 'ci' else [None, ValueError('superseded')] if gate == 'superseded' else [None]
            with self.subTest(gate=gate), patch.object(release, 'fetch_revision', return_value=NEW), patch.object(release, 'require_approved', side_effect=approvals), patch.object(release, 'backend_ready', side_effect=ValueError('backend pending') if gate == 'backend' else None), patch.object(release, 'build', return_value=built_files(NEW)) as build, patch.object(release, 'remote') as remote:
                with self.assertRaises(ValueError):
                    release.reconcile(check_only=True)
                remote.assert_not_called()
                if gate != 'superseded':
                    build.assert_not_called()

    def test_already_current_release_recovers_lost_success_without_reactivation(self):
        manifest, files = built_files(NEW)
        previous = {'files': {name: installer.digest(data) for name, data in files.items()}, 'current': '/existing/release'}
        with patch.object(release, 'fetch_revision', return_value=NEW), patch.object(release, 'require_approved'), patch.object(release, 'backend_ready'), patch.object(release, 'build', return_value=(manifest, files)), patch.object(release, 'remote', return_value=previous) as remote, patch.object(release, 'verify_frontend'), patch.object(release, 'send_file') as send:
            release.reconcile()
            remote.assert_called_once_with('--inspect')
            send.assert_not_called()
            self.assertEqual(json.loads((release.BASE / 'state.json').read_text())['deployed_sha'], NEW)

    def test_package_is_reproducible_and_only_transfers_changed_files(self):
        previous = installer.inspect()
        manifest, files = built_files(NEW)
        first, parameters = release.package(manifest, files, previous)
        self.assertEqual(first, release.package(manifest, files, previous)[0])
        with tarfile.open(fileobj=io.BytesIO(first), mode='r:gz') as source:
            self.assertEqual(set(source.getnames()), set(files) - set(parameters['reused_files']))

    def test_external_verification_failure_rolls_back_and_never_marks_deployed(self):
        manifest, files = built_files(NEW)
        previous = installer.inspect()
        with patch.object(release, 'fetch_revision', return_value=NEW), patch.object(release, 'require_approved'), patch.object(release, 'backend_ready'), patch.object(release, 'build', return_value=(manifest, files)), patch.object(release, 'remote', side_effect=[previous, {'backup': '/owned/backup'}, {'restored': str(self.old)}]) as remote, patch.object(release, 'verify_frontend', side_effect=ValueError('public file mismatch')), patch.object(release, 'send_file'):
            with self.assertRaisesRegex(ValueError, 'public file mismatch'):
                release.reconcile()
            self.assertEqual(remote.call_args.args, ('--rollback', '/owned/backup'))
            self.assertNotIn('deployed_sha', json.loads((release.BASE / 'state.json').read_text()))


class FrontendControllerTest(unittest.TestCase):
    def test_cloud_receipt_is_not_delivery_and_failed_delivery_is_not_success(self):
        def record(status):
            return {'Invocations': {'Invocation': [{'InvokeInstances': {'InvokeInstance': [{'InvocationStatus': status}]}}]}}
        with patch.object(release, 'cloud', side_effect=[record('Running'), record('Success')]) as api, patch.object(release.time, 'sleep'):
            release.wait_cloud('receipt', send=True)
            self.assertEqual(api.call_count, 2)
        with patch.object(release, 'cloud', return_value=record('Failed')):
            with self.assertRaisesRegex(RuntimeError, 'delivery failed'):
                release.wait_cloud('receipt', send=True)

    def test_build_has_no_credentials_or_socket_and_timeout_removes_only_owned_container(self):
        with patch.object(release.subprocess, 'run', side_effect=[subprocess.TimeoutExpired('docker', 600), subprocess.CompletedProcess([], 0)]) as run:
            with self.assertRaises(subprocess.TimeoutExpired):
                release.container(Path('/owned/build'), ['npm', 'run', 'build'], 'none')
            args = run.call_args_list[0].args[0]
            for option in ['--network=none', '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges']:
                self.assertIn(option, args)
            self.assertEqual(args[args.index('--mount') + 1], 'type=bind,src=/owned/build,dst=/work')
            self.assertNotIn('docker.sock', ' '.join(args))
            self.assertNotIn('.aliyun', ' '.join(args))
            name = args[args.index('--name') + 1]
            self.assertEqual(run.call_args_list[-1].args[0], ['docker', 'rm', '-f', name])


if __name__ == '__main__':
    unittest.main()
