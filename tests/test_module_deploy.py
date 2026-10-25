import copy
import importlib.util
import json
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(ROOT/'ops'), str(ROOT/'ops/lab')]
import app
import check_network
import deployment_health
import object_store
import services
import verify_deployment


class ModuleDeployTest(unittest.TestCase):
    def test_catalog_probe_uses_actual_id_limits_and_cleans_up_on_timeout(self):
        image_id='sha256:'+'a'*64
        for timeout in [False,True]:
            with self.subTest(timeout=timeout),tempfile.TemporaryDirectory() as tmp,patch.object(app,'STATE',Path(tmp)),patch.object(app,'run',return_value=subprocess.CompletedProcess([],0,stdout=image_id+'\n')):
                def command(args,**kwargs):
                    if args[1]=='run' and timeout:raise subprocess.TimeoutExpired(args,15)
                    return subprocess.CompletedProcess(args,0,stdout='voting-catalog-v1\n')
                with patch.object(app.subprocess,'run',side_effect=command) as run:
                    self.assertEqual(app.catalog_image_supported('human-worth/content:candidate',image_id),not timeout)
                    probe=run.call_args_list[0].args[0]
                    for flag in ['--network=none','--read-only','--pids-limit=16','--cpus=0.25','--memory=64m']:
                        self.assertIn(flag,probe)
                    self.assertEqual(probe[-2:],[image_id,'catalog-protocol-version'])
                    self.assertEqual(run.call_args_list[-1].args[0],['docker','rm','-f',probe[probe.index('--name')+1]])
                    self.assertEqual((Path(tmp)/'catalog-capabilities.json').exists(),not timeout)

    def test_catalog_probe_rejects_wrong_id_output_and_retag(self):
        image_id='sha256:'+'a'*64
        for ids,expected,output in [([image_id],'sha256:'+'b'*64,'voting-catalog-v1'),
                                    ([image_id],None,'unknown'),
                                    ([image_id,'sha256:'+'b'*64],None,'voting-catalog-v1')]:
            with self.subTest(ids=ids,output=output),tempfile.TemporaryDirectory() as tmp,patch.object(app,'STATE',Path(tmp)),patch.object(app,'run',side_effect=[subprocess.CompletedProcess([],0,stdout=value) for value in ids]),patch.object(app.subprocess,'run',return_value=subprocess.CompletedProcess([],0,stdout=output)):
                self.assertFalse(app.catalog_image_supported('human-worth/content:candidate',expected))
                self.assertFalse((Path(tmp)/'catalog-capabilities.json').exists())

    def test_first_catalog_activation_drains_legacy_and_restart_still_drains(self):
        def deployment(image):
            return {'kind':'Deployment','metadata':{'name':'content'},'spec':{'template':{'spec':{'containers':[{'image':image,'env':[{'name':'VOTING_TARGET','value':'voting'}]}]}}}}
        new=deployment('new');old=deployment('old')
        with tempfile.TemporaryDirectory() as tmp,patch.object(app,'STATE',Path(tmp)),patch.object(app,'catalog_image_supported',side_effect=lambda image: image=='new'):
            for attempt in range(2):
                def kube(*args,**kwargs):
                    if 'get' in args:
                        return subprocess.CompletedProcess(args,0,stdout=json.dumps({'items':[{'spec':{'containers':[{'image':'old'}]}}]} if 'pods' in args else old))
                    if '--for=delete' in args and attempt==0:raise RuntimeError('drain interrupted')
                with self.subTest(attempt=attempt),patch.object(app,'kube',side_effect=kube) as command:
                    if attempt==0:
                        with self.assertRaisesRegex(RuntimeError,'drain interrupted'):app.prepare_catalog_activation({'content':[new],'voting':[]})
                    else:app.prepare_catalog_activation({'content':[new],'voting':[]})
                    self.assertTrue(app.catalog_enabled())
                    self.assertEqual(command.call_args_list[-2].args,('-n','human-worth','scale','deployment/content','--replicas=0'))
                    self.assertIn('--for=delete',command.call_args_list[-1].args)
            # Compatible rolling updates do not cause a second outage.
            with patch.object(app,'kube',side_effect=lambda *args,**kwargs:subprocess.CompletedProcess(args,0,stdout=json.dumps({'items':[{'spec':{'containers':[{'image':'new'}]}}]} if 'pods' in args else new))) as command:
                app.prepare_catalog_activation({'content':[new],'voting':[]})
                self.assertEqual(command.call_count,2)
            with self.assertRaisesRegex(RuntimeError,'without Voting'):app.prepare_catalog_activation({'content':[new]})

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
        for name in ['identity', 'content', 'asset', 'moderation']:
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

    def test_worker_token_projection_cannot_expand_to_other_credentials(self):
        image='human-worth/challenge-worker:'+'a'*40
        pod={'serviceAccountName':'challenge-worker','automountServiceAccountToken':False,'containers':[{'name':'challenge-worker','image':image,'startupProbe':{'test':True},'livenessProbe':{'test':True},'readinessProbe':{'test':True}}],
             'volumes':[{'name':'api','projected':{'defaultMode':0o440,'sources':[{'serviceAccountToken':{'path':'token','expirationSeconds':3600}},{'configMap':{'name':'kube-root-ca.crt','items':[{'key':'ca.crt','path':'ca.crt'}]}}]}}]}
        objects=[{'kind':kind,'metadata':{'name':'challenge-worker','namespace':'human-worth'}} for kind in ['Deployment','ServiceAccount','NetworkPolicy']]
        objects[0]['spec']={'replicas':2,'selector':{'matchLabels':{'app':'challenge-worker'}},'template':{'metadata':{'labels':{'app':'challenge-worker'}},'spec':pod}}
        objects[1]['automountServiceAccountToken']=False
        objects[2]['spec']={'podSelector':{'matchLabels':{'app':'challenge-worker'}}}
        services.validate_manifest(objects,'challenge-worker',image)
        for mutate in [lambda p:p.update(automountServiceAccountToken=True),
                       lambda p:p['volumes'][0]['projected']['sources'].append({'secret':{'name':'identity-runtime'}}),
                       lambda p:p['volumes'][0]['projected']['sources'][0]['serviceAccountToken'].update(expirationSeconds=86400)]:
            changed=copy.deepcopy(objects);mutate(changed[0]['spec']['template']['spec'])
            with self.assertRaises(ValueError):services.validate_manifest(changed,'challenge-worker',image)

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

    def test_public_gate_checks_private_submission_list(self):
        checks = [check for spec in services.load_services(ROOT).values() for check in spec['checks']]
        self.assertIn({'path':'/api/me/submissions','status':401,'code':'unauthenticated'}, checks)
        sha = 'a'*40
        ready = (200, {'status':'ok','service':'human-worth','revision':sha})

        def response(path, options=()):
            if path == '/api/health':
                return ready
            if path.endswith('/voting-eligibility'):
                return 401, {'code':'unauthenticated'}
            if path in ('/api/tasks','/api/board'):return 200, {}
            if path.endswith('/ballot'):return 401, {'code':'unauthenticated'}
            if path.startswith('/api/tasks/'):
                return 404, {'code':'asset_not_found' if path.endswith('/content') else 'task_not_found'}
            return 401, {'code':'unauthenticated'}

        for failure in [(503,{'code':'content_unavailable'}), (404,{'code':'not_found'}), (401,{'code':'wrong_error'})]:
            with patch.object(deployment_health,'response',side_effect=lambda path, options=():failure if path == '/api/me/submissions' else response(path, options)):
                self.assertFalse(deployment_health.healthy(sha, checks))
        with patch.object(deployment_health,'response',side_effect=response):
            self.assertTrue(deployment_health.healthy(sha, checks))

    def test_public_browse_probes_reject_outages_and_false_success(self):
        checks = [c for c in services.load_services(ROOT)['content']['checks'] if c['path'].startswith('/api/tasks/')]
        self.assertEqual(len(checks), 2)
        self.assertTrue(any(c['path'].endswith('/entries') for c in checks))
        sha = 'a'*40
        for public in checks:
            for reply, expected in [((404, {'code':'task_not_found'}), True),
                                    ((503, {'code':'content_unavailable'}), False),
                                    ((200, {}), False), ((404, {'code':'asset_not_found'}), False)]:
                def response(path, options=()):
                    if path == '/api/health': return 200, {'status':'ok','service':'human-worth','revision':sha}
                    if path == public['path']: return reply
                    return 404, {'code':'task_not_found'}
                with patch.object(deployment_health,'response',side_effect=response):
                    self.assertEqual(deployment_health.healthy(sha, checks),expected)

    def test_public_file_probe_rejects_outage_and_wrong_denial(self):
        checks = services.load_services(ROOT)['asset']['checks']
        public = next(c for c in checks if c['path'].startswith('/api/tasks/'))
        self.assertEqual((public['status'], public['code']), (404, 'asset_not_found'))
        sha = 'a'*40
        def response(path, options=()):
            if path == '/api/health': return 200, {'status':'ok','service':'human-worth','revision':sha}
            return 401, {'code':'unauthenticated'}
        for reply, expected in [((404, {'code':'asset_not_found'}), True),
                                ((503, {'code':'asset_unavailable'}), False),
                                ((200, {}), False), ((404, {'code':'not_found'}), False)]:
            with patch.object(deployment_health, 'response', side_effect=lambda path, options=(): reply if path == public['path'] else response(path, options)):
                self.assertEqual(deployment_health.healthy(sha, checks), expected)

    def test_asset_catalog_and_private_s3_wiring(self):
        actual=services.load_services(ROOT)
        self.assertEqual(actual['asset']['dependencies'], ['identity'])
        self.assertIn('asset',actual['content']['dependencies'])
        self.assertIn('asset',actual['gateway']['dependencies'])
        self.assertIn('challenge',actual)
        self.assertIn('challenge-worker',actual)
        self.assertIn({'path':'/api/me/assets','status':401,'code':'unauthenticated'},actual['asset']['checks'])
        manifest=(ROOT/'ops/lab/asset.yaml').read_text()
        for value in ['replicas: 2', 'ASSET_STORAGE_BACKEND, value: s3', 'secretName: asset-runtime',
                      'secretName: asset-tls', 'secretName: database-ca', 'readOnlyRootFilesystem: true',
                      'runAsNonRoot: true', 'automountServiceAccountToken: false', 'port: 8333']:
            self.assertIn(value,manifest)
        self.assertNotIn('ASSET_STORAGE_DIR',manifest)
        for name in ['gateway','content']:
            self.assertIn('ASSET_TARGET, value: "dns:///asset.human-worth.svc.cluster.local:8443"',(ROOT/('ops/lab/'+name+'.yaml')).read_text())
        self.assertIn('app: asset',(ROOT/'ops/lab/identity.yaml').read_text())
        self.assertIn('CONTENT_TARGET, value: "dns:///content.human-worth.svc.cluster.local:8443"',manifest)
        # Public authorization is a per-request dependency, not startup readiness:
        # asset -> content -> asset would otherwise deadlock deployment.
        self.assertIn('app: content',manifest)
        self.assertIn('app: asset',(ROOT/'ops/lab/content.yaml').read_text())
        grants=(ROOT/'ops/asset-grants.sql').read_text()
        self.assertIn('storage_cleaned_at',grants)
        for value in ['GRANT DELETE','GRANT CREATE','GRANT ALL','identity.','content.','UPDATE(owner_id']:
            self.assertNotIn(value,grants)

    def test_validator_requires_asset_and_content_business_tests_on_snapshot(self):
        script = (ROOT/'ops/lab/validate_modules.py').read_text()
        selection = re.search(r"'-run','([^']+)'", script)
        self.assertIsNotNone(selection)
        for name in ['TestKindAssetDeployment', 'TestKindContentDeployment', 'TestKindPublicAssetDeployment']:
            self.assertRegex(name, selection.group(1))
        self.assertNotRegex('TestUnrelated', selection.group(1))
        self.assertIn("cwd=source/'backend'", script)
        self.assertNotIn("cwd=app.REPO/'backend'", script)

    def test_network_probe_permissions_are_independent_of_private_host_umask(self):
        callers = []

        def run(*args):
            if args[:3] == ('env', 'CGO_ENABLED=0', 'go'):
                binary = Path(args[args.index('-o')+1])
                binary.write_bytes(b'test-only executable placeholder')
                binary.chmod(0o700)
            if args[:2] == ('docker', 'build'):
                directory = Path(args[-1])
                self.assertEqual((directory/'probe').stat().st_mode & 0o777, 0o755)
                self.assertEqual((directory/'ca-certificates.crt').stat().st_mode & 0o777, 0o644)

        def apply(raw):
            pod = json.loads(raw)
            callers.append(pod['metadata']['labels']['app'])
            self.assertTrue(pod['spec']['securityContext']['runAsNonRoot'])
            self.assertFalse(pod['spec']['automountServiceAccountToken'])
            if callers[-1] == 'challenge-worker':
                self.assertEqual(pod['spec']['containers'][0]['env'], [{'name': 'MODEL_TEST_HOST', 'value': 'model.example.test'}])

        def kube(*args, **kwargs):
            output = json.dumps({'data': {'squid.conf': 'acl model_hosts dstdomain model.example.test\n'}}) if 'configmap' in args else 'Succeeded'
            return subprocess.CompletedProcess(args, 0, stdout=output)

        with patch.object(check_network, 'run', side_effect=run), patch.object(check_network, 'apply', side_effect=apply), patch.object(check_network, 'kube', side_effect=kube):
            check_network.main()
        self.assertEqual(callers, ['identity', 'content', 'asset', 'moderation', 'voting', 'discovery', 'challenge', 'challenge-worker', 'gateway', 'untrusted'])

    def test_asset_store_bucket_owner_is_confirmed(self):
        config={'endpoint':'http://asset-store.human-worth.svc.cluster.local:8333',
                'bucket':'test-assets','region':'us-east-1','access_key':'test-access',
                'secret_key':'test-secret','force_path_style':True}
        calls=[]
        # SeaweedFS 4.13 prints prompts even for non-TTY, piped input. This
        # transcript reproduces the real kind Pod's bucket list output.
        transcript=('master: localhost:9333 filer: 127.0.0.1:8888\n'
                    '> create bucket under /buckets\ncreated bucket test-assets\n'
                    'bucket owner: asset-runtime\n'
                    '>   test-assets\tsize:0\tchunk:0\towner:"asset-runtime"\n> ')
        for output in [transcript, '  test-assets\tsize:0\tchunk:0\towner:"asset-runtime"\n']:
            def kube(*args, **kwargs):
                calls.append((args, kwargs))
                return subprocess.CompletedProcess(args, 0, stdout=output if 'exec' in args else '')
            with self.subTest(output=output), patch.object(object_store.app,'secret'), patch.object(object_store,'apply'), patch.object(object_store,'kube',side_effect=kube):
                object_store.install(config)
        command=next(kwargs['input'] for args, kwargs in calls if 'exec' in args)
        self.assertIn('s3.bucket.create -name test-assets -owner asset-runtime',command)
        self.assertIn('s3.bucket.list',command)
        for output in ['bucket command failed\n', 'created bucket test-assets\n',
                       transcript.replace('test-assets\t','other-assets\t'),
                       transcript.replace('owner:"asset-runtime"','owner:"other-owner"'),
                       transcript.replace('\towner:"asset-runtime"','')]:
            with self.subTest(output=output), patch.object(object_store.app,'secret'), patch.object(object_store,'apply'), patch.object(object_store,'kube',return_value=subprocess.CompletedProcess([],0,stdout=output)):
                with self.assertRaisesRegex(RuntimeError,'did not confirm'):
                    object_store.install(config)

    def test_asset_secrets_fail_closed_and_never_cross_modules(self):
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory)
            with patch.dict(app.os.environ,{'ASSET_S3_CONFIG_FILE':''}):
                # An explicit empty override is also invalid; no generated fallback.
                with self.assertRaises(RuntimeError):app.object_storage_config(state)
            config={'endpoint':'http://asset-store.human-worth.svc.cluster.local:8333',
                    'bucket':'test-assets','region':'us-east-1','access_key':'test-access',
                    'secret_key':'test-secret','force_path_style':True}
            path=state/'asset-s3.json';path.write_text(json.dumps(config));path.chmod(0o600)
            with patch.dict(app.os.environ,{'ASSET_S3_CONFIG_FILE':str(path)}):
                self.assertEqual(app.object_storage_config(state),config)
                path.chmod(0o644)
                with self.assertRaises(RuntimeError):app.object_storage_config(state)
                path.chmod(0o600)
                changed={**config,'endpoint':'https://public.example'};path.write_text(json.dumps(changed))
                with self.assertRaises(RuntimeError):app.object_storage_config(state)
            captured={}
            with patch.object(app,'STATE',state),patch.object(app,'secret',side_effect=lambda name,values,*args:captured.update({name:values})):
                app.database_secrets({'asset':{'database':True},'content':{'database':True}},config)
            self.assertEqual(captured['asset-runtime']['s3-access-key'],'test-access')
            self.assertEqual(captured['asset-runtime']['s3-force-path-style'],'true')
            self.assertEqual(captured['content-runtime'].keys(),{'database-url'})
            self.assertEqual(captured['asset-owner'].keys(),{'database-url'})

    def test_superseded_or_failed_ci_never_reports_deployment_success(self):
        sha='a'*40
        with patch.object(verify_deployment,'github_json',return_value={'object':{'sha':'b'*40}}):
            with self.assertRaisesRegex(RuntimeError,'superseded'):verify_deployment.require_current_ci(sha)
        with patch.object(verify_deployment,'github_json',side_effect=[{'object':{'sha':sha}},{'workflow_runs':[]}]):
            with self.assertRaisesRegex(RuntimeError,'not successful'):verify_deployment.require_current_ci(sha)


if __name__=='__main__': unittest.main()
