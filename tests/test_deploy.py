import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('deploy', Path(__file__).resolve().parents[1] / 'ops/deploy.py')
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


class DeployGateTest(unittest.TestCase):
    def run_record(self, **changes):
        record = dict(id=1, run_attempt=1, head_sha='abc', head_branch='dev', event='push',
                      path='.github/workflows/ci.yml', head_repository={'full_name': deploy.REPO},
                      status='completed', conclusion='success')
        return record | changes

    def test_only_matching_successful_push_ci_deploys(self):
        self.assertTrue(deploy.ci_passed([self.run_record()], 'abc'))
        for changes in [dict(event='pull_request'), dict(head_branch='main'), dict(head_sha='old'),
                        dict(conclusion='failure'), dict(status='in_progress'),
                        dict(head_repository={'full_name': 'fork/repo'}), dict(path='different.yml')]:
            self.assertFalse(deploy.ci_passed([self.run_record(**changes)], 'abc'))
        self.assertFalse(deploy.ci_passed([], 'abc'))

    def test_newer_failed_or_pending_run_does_not_reuse_old_success(self):
        for conclusion, status in [('failure', 'completed'), (None, 'in_progress')]:
            self.assertFalse(deploy.ci_passed([
                self.run_record(), self.run_record(id=2, conclusion=conclusion, status=status)], 'abc'))

    def test_release_archive_cannot_write_outside_release_or_create_symlinks(self):
        for name, kind in [('../outside', tarfile.REGTYPE), ('/outside', tarfile.REGTYPE), ('link', tarfile.SYMTYPE)]:
            archive = io.BytesIO()
            with tarfile.open(fileobj=archive, mode='w') as tar:
                member = tarfile.TarInfo(name)
                member.type = kind
                member.linkname = '/etc/passwd'
                tar.addfile(member)
            with tempfile.TemporaryDirectory() as folder:
                with self.assertRaises(ValueError):
                    deploy.extract_release(archive.getvalue(), Path(folder))


if __name__ == '__main__':
    unittest.main()
