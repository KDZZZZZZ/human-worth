import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(ROOT/'ops'), str(ROOT/'ops/lab')]
import app
import deployment_health
import services
import verify_deployment


class ModuleDeployTest(unittest.TestCase):
    def test_catalog_orders_dependencies_and_requires_real_module_inputs(self):
        actual = services.load_services(ROOT)
        self.assertEqual(list(actual)[-1], 'gateway')
        for name, spec in actual.items():
            for dependency in spec['dependencies']:
                self.assertLess(list(actual).index(dependency), list(actual).index(name))
        catalog = {name:copy.deepcopy(actual[name]) for name in ['identity','content','gateway']}
        for name, dependencies in [('identity',[]),('content',['identity']),('gateway',['identity','content'])]:
            catalog[name]['dependencies']=dependencies
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for path in ['ops/lab/services.json', *[p for name, spec in catalog.items() for p in ['backend/cmd/'+name+'/main.go', 'ops/lab/'+name+'.yaml', *([spec['grants']] if spec['database'] else [])]]]:
                target = root/path; target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes((ROOT/path).read_bytes())
            catalog['asset'] = dict(dependencies=['identity'], database=True, grants='ops/asset/grants.sql', checks=[])
            catalog['gateway']['dependencies'].append('asset')
            source = root/'ops/lab/services.json'; source.write_text(json.dumps(catalog))
            with self.assertRaisesRegex(ValueError, 'Missing'): services.load_services(root)
            for path in ['backend/cmd/asset/main.go', 'ops/lab/asset.yaml', 'ops/asset/grants.sql']:
                target = root/path; target.parent.mkdir(parents=True, exist_ok=True); target.write_text('test-only input')
            order = list(services.load_services(root))
            self.assertLess(order.index('asset'), order.index('gateway'))
            catalog['asset']['dependencies'] = ['gateway']; source.write_text(json.dumps(catalog))
            with self.assertRaises(ValueError): services.load_services(root)
            catalog['asset']['dependencies'] = ['missing']; source.write_text(json.dumps(catalog))
            with self.assertRaisesRegex(ValueError, 'dependency'): services.load_services(root)
            catalog['asset']['dependencies'] = []; catalog['asset']['grants'] = '../outside'; source.write_text(json.dumps(catalog))
            with self.assertRaisesRegex(ValueError, 'unsafe'): services.load_services(root)

    def test_new_database_module_gets_only_its_roles_and_network_paths(self):
        catalog = services.load_services(ROOT)
        catalog['asset'] = dict(database=True)
        manifest = app.database_manifest(catalog)
        for name in ['identity', 'content', 'asset']:
            self.assertIn('name: '+name+'_owner', manifest)
            self.assertIn('name: '+name+'_runtime', manifest)
        self.assertNotIn('name: gateway_owner', manifest)
        self.assertIn('app: asset-migrate', manifest)
        self.assertNotIn('app: gateway', manifest)
        job = app.maintenance_job('content-migration', 'content-migrate', 'image', ['migrate'], 'content-owner', service='content')
        pod = job['spec']['template']['spec']
        self.assertFalse(pod['automountServiceAccountToken'])
        self.assertEqual(pod['containers'][0]['env'][0]['name'], 'CONTENT_DATABASE_URL_FILE')
        self.assertEqual(pod['volumes'][0]['secret']['secretName'], 'content-owner')

    def test_failed_migration_is_recreated_but_completed_job_is_preserved(self):
        for status, deleted in [('Failed', True), ('Complete', False)]:
            record = json.dumps({'status': {'conditions': [{'type': status, 'status': 'True'}]}})
            with patch.object(app, 'kube', return_value=subprocess.CompletedProcess([], 0, stdout=record)) as kube, patch.object(app, 'apply'):
                app.migration('human-worth/content:'+'a'*40, 'content')
                self.assertEqual(any('delete' in call.args for call in kube.call_args_list), deleted)
                self.assertIn('job/content-migrate-'+'a'*40, kube.call_args.args)

    def test_grants_run_as_owner_and_do_not_allow_psql_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); (root/'content-owner.password').write_text('test-password')
            sql=root/'grants.sql'; sql.write_text('GRANT SELECT ON content.task_drafts TO content_runtime;')
            with patch.object(app, 'REPO', root), patch.object(app, 'STATE', root), patch.object(app, 'kube') as kube:
                app.grant_runtime('primary', 'content', {'grants':'grants.sql'})
                self.assertNotIn('test-password', str(kube.call_args.args))
                self.assertIn('postgresql://content_owner@127.0.0.1/human_worth?sslmode=require', kube.call_args.args)
                sql.write_text('\\! echo forbidden')
                with self.assertRaisesRegex(ValueError, 'meta-commands'): app.grant_runtime('primary', 'content', {'grants':'grants.sql'})

    def test_module_cannot_take_over_infrastructure_or_read_identity_keys(self):
        image='human-worth/content:'+'a'*40
        pod={'serviceAccountName':'content','automountServiceAccountToken':False,'containers':[{'name':'content','image':image,'startupProbe':{'test':True},'livenessProbe':{'test':True},'readinessProbe':{'test':True}}]}
        objects=[{'kind':kind,'metadata':{'name':'content','namespace':'human-worth'}} for kind in ['Deployment','ServiceAccount','NetworkPolicy']]
        objects[0]['spec']={'replicas':2,'selector':{'matchLabels':{'app':'content'}},'template':{'metadata':{'labels':{'app':'content'}},'spec':pod}}
        objects[1]['automountServiceAccountToken']=False
        objects[2]['spec']={'podSelector':{'matchLabels':{'app':'content'}}}
        services.validate_manifest(objects,'content',image)
        for mutation in [lambda v:v[0]['metadata'].update(namespace='kube-system'),
                         lambda v:v[0].update(kind='ClusterRoleBinding'),
                         lambda v:v[0]['spec']['template']['spec']['containers'][0].update(image='unapproved'),
                         lambda v:v[0]['spec']['template']['spec'].update(volumes=[{'name':'keys','secret':{'secretName':'identity-runtime'}}]),
                         lambda v:v[2]['spec'].update(podSelector={})]:
            changed=copy.deepcopy(objects);mutation(changed)
            with self.assertRaises(ValueError): services.validate_manifest(changed,'content',image)

    def test_public_gate_rejects_missing_content_wrong_revision_and_transient_failure(self):
        sha='a'*40
        ready=(200, {'status':'ok','service':'human-worth','revision':sha})
        checks=[{'path':'/api/me/tasks/test','status':401,'code':'unauthenticated'}]
        for reply, expected in [((503,{'code':'content_unavailable'}),False),((401,{'code':'unauthenticated'}),True),((401,{'code':'wrong_error'}),False)]:
            with patch.object(deployment_health,'response',side_effect=[ready,reply,ready]):
                self.assertEqual(deployment_health.healthy(sha,checks),expected)
        with patch.object(deployment_health,'response',return_value=ready):
            self.assertFalse(deployment_health.healthy('b'*40))
            self.assertFalse(deployment_health.healthy(sha,deployed_services=['identity','content','gateway']))
        complete=(200, {**ready[1],'deploymentRevision':sha,'deployedServices':['identity','content','gateway']})
        with patch.object(deployment_health,'response',return_value=complete):
            self.assertTrue(deployment_health.healthy(sha,deployed_services=['identity','content','gateway']))
            self.assertFalse(deployment_health.healthy(sha,deployed_services=['identity','content','gateway','challenge-worker']))
        with patch.object(verify_deployment,'require_current_ci') as gate,patch.object(verify_deployment,'healthy',side_effect=[False,True]),patch.object(verify_deployment.time,'sleep'):
            verify_deployment.wait_for_deployment(sha,checks)
            self.assertEqual(gate.call_count,3)
        with patch.object(verify_deployment,'require_current_ci'),patch.object(verify_deployment,'healthy',return_value=False):
            with self.assertRaisesRegex(RuntimeError,'timed out'):verify_deployment.wait_for_deployment(sha,checks,timeout=0)

    def test_superseded_or_failed_ci_never_reports_deployment_success(self):
        sha='a'*40
        with patch.object(verify_deployment,'github_json',return_value={'object':{'sha':'b'*40}}):
            with self.assertRaisesRegex(RuntimeError,'superseded'):verify_deployment.require_current_ci(sha)
        with patch.object(verify_deployment,'github_json',side_effect=[{'object':{'sha':sha}},{'workflow_runs':[]}]):
            with self.assertRaisesRegex(RuntimeError,'not successful'):verify_deployment.require_current_ci(sha)


if __name__=='__main__': unittest.main()
