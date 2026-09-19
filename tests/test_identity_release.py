import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT/'ops/lab'))
spec=importlib.util.spec_from_file_location('identity_release',ROOT/'ops/lab/release.py')
release=importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class IdentityReleaseTest(unittest.TestCase):
    def test_health_wait_handles_transient_failure_and_has_a_deadline(self):
        with patch.object(release,'health',side_effect=[False,False,True]),patch.object(release.time,'sleep') as sleep:
            self.assertTrue(release.wait_health('approved'))
            self.assertEqual(sleep.call_count,2)
        with patch.object(release,'health',return_value=False),patch.object(release.time,'monotonic',side_effect=[0,46]),patch.object(release.time,'sleep') as sleep:
            self.assertFalse(release.wait_health('approved'))
            sleep.assert_not_called()

    def test_rollback_uses_the_same_field_manager_as_releases(self):
        deployment={'metadata':{'name':'gateway'},'spec':{'replicas':2}}
        with patch.object(release,'kube') as kube:
            release.restore([deployment],None)
            self.assertIn('--field-manager=human-worth-release',kube.call_args_list[0].args)

    def test_gate_rejects_changed_head_failed_ci_and_malformed_sha(self):
        sha='a'*40
        record=dict(id=1,head_sha=sha,head_branch='dev',event='push',path='.github/workflows/ci.yml',head_repository={'full_name':release.REPO},status='completed',conclusion='success')
        with patch.object(release,'command',return_value=sha+' refs/heads/dev'),patch.object(release,'read_json',return_value={'workflow_runs':[record]}):
            release.require_approved(sha)
            record['conclusion']='failure'
            with self.assertRaisesRegex(RuntimeError,'push CI'):release.require_approved(sha)
        with patch.object(release,'command',return_value='b'*40+' refs/heads/dev'),patch.object(release,'read_json') as lookup:
            with self.assertRaisesRegex(RuntimeError,'current dev'):release.require_approved(sha)
            lookup.assert_not_called()
        with patch.object(release,'command') as command:
            with self.assertRaises(ValueError):release.require_approved('../candidate')
            command.assert_not_called()

    def test_old_foundation_or_check_only_cannot_activate(self):
        sha='a'*40
        with tempfile.TemporaryDirectory() as tmp,patch.object(release,'BASE',Path(tmp)),patch.object(release,'fetch_revision',return_value=sha),patch.object(release,'require_approved'),patch.object(release.app,'main') as activate:
            path=Path(tmp)/'releases'/sha
            path.mkdir(parents=True)
            with self.assertRaisesRegex(RuntimeError,'does not contain Identity'):release.reconcile()
            (path/'backend/cmd/identity').mkdir(parents=True)
            (path/'backend/cmd/identity/main.go').write_text('package main')
            release.reconcile(check_only=True)
            activate.assert_not_called()

    def test_failed_activation_restores_the_previous_application(self):
        sha='a'*40
        previous=[{'metadata':{'name':name},'spec':{'template':{'spec':{'containers':[{'image':'human-worth/'+name+':previous'}]}}}} for name in ['identity','gateway']]
        with tempfile.TemporaryDirectory() as tmp:
            base=Path(tmp);state=base/'private';state.mkdir();(state/'build.json').write_text('{"previous":true}')
            path=base/'releases'/sha/'backend/cmd/identity';path.mkdir(parents=True);(path/'main.go').write_text('package main')
            with patch.object(release,'BASE',base),patch.object(release,'STATE',state),patch.object(release,'fetch_revision',return_value=sha),patch.object(release,'require_approved'),patch.object(release.subprocess,'run',return_value=subprocess.CompletedProcess([],0)),patch.object(release,'snapshot',return_value=previous),patch.object(release,'restore') as restore,patch.object(release,'health',return_value=True),patch.object(release.app,'REPO',base),patch.object(release.app,'main',side_effect=RuntimeError('unhealthy')):
                with self.assertRaisesRegex(RuntimeError,'restored'):release.reconcile()
                restore.assert_called_once_with(previous,'{"previous":true}')
                self.assertFalse((state/'release-revision').exists())
                self.assertEqual(json.loads((base/'state.json').read_text())['failed_sha'],sha)

    def test_public_lab_refuses_unapproved_working_tree_activation(self):
        with tempfile.TemporaryDirectory() as tmp,patch.object(release.app,'STATE',Path(tmp)),patch.object(release.app,'database_secrets') as secrets:
            (Path(tmp)/'release-revision').write_text('a'*40)
            with self.assertRaisesRegex(RuntimeError,'working-tree'):release.app.main()
            secrets.assert_not_called()


if __name__=='__main__':unittest.main()
