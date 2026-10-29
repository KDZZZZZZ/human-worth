#!/usr/bin/env python3
"""Exercise module deployment in a disposable namespace of the existing kind lab.

Uses the real manifests/controller, an isolated single-instance PostgreSQL and
private, dynamically allocated Gateway NodePort. Never changes the public
namespace, its fixed NodePort or its secrets.
"""
import base64
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
from unittest.mock import patch

import app
import infra
import object_store
import release
from deployment_health import healthy


def objects(raw):
    decoder=json.JSONDecoder()
    result=[]
    while raw.strip():
        obj, end=decoder.raw_decode(raw.lstrip())
        result.extend(obj['items'] if obj.get('kind')=='List' else [obj])
        raw=raw.lstrip()[end:]
    return result


def catalog_upgrade_regression(kube, state, legacy_source, legacy_revision, revision, cluster="lab"):
    """Real v6 Pods -> fenced v7, with real queued/applied bootstrap faults.

    The historical reviewed task is an explicit SQL projection fixture. Its
    visibility is checked through the real v6 HTTP service; formal review HTTP
    remains covered by the existing Go lab suite below.
    """
    with patch.object(app,'REPO',legacy_source):app.main(revision=legacy_revision)
    previous=release.snapshot()
    old_content=next(p for p in previous if p['kind']=='Deployment' and p['metadata']['name']=='content')
    old_image=old_content['spec']['template']['spec']['containers'][0]['image']
    if app.catalog_image_supported(old_image):raise RuntimeError('Real v6 unexpectedly advertises catalog-v1')
    primary=kube('-n','human-worth','get','cluster','database','-o','jsonpath={.status.currentPrimary}',capture=True).stdout.strip()
    def sql(query):
        return kube('-n','human-worth','exec',primary,'--','psql','-X','-U','postgres','-d','human_worth','-At','-v','ON_ERROR_STOP=1','-c',query,capture=True).stdout.strip()
    task='tsk_'+'f1'*16
    content={'title':'real v6 upgrade baseline','summary':'','description':'historical projection fixture','entries':[
        {'side':side,'title':side,'source':'self','artifacts':[{'kind':'link','url':'https://example.test/'+side}],
         'permissions':{'cloudUse':False}} for side in ['human','agent']]}
    raw=json.dumps(content)
    sql("INSERT INTO content.task_drafts(id,author_id,create_key,create_hash,state,content) VALUES('"+task+"','upgrade-fixture','upgrade-baseline-01',decode(repeat('00',32),'hex'),'published','"+raw+"');"
        "INSERT INTO content.reviewed_tasks(task_id,source_revision,revision,state,snapshot) VALUES('"+task+"',1,1,'published','"+raw+"');"
        "INSERT INTO content.reviewed_entries(id,task_id,position,state) VALUES('ent_"+'f2'*16+"','"+task+"',0,'approved'),('ent_"+'f3'*16+"','"+task+"',1,'approved');")
    gateway=json.loads(kube('-n','human-worth','get','service','gateway','-o','json',capture=True).stdout)
    port=gateway['spec']['ports'][0]['nodePort']
    node=subprocess.check_output(['docker','inspect',cluster+'-control-plane','--format','{{(index .NetworkSettings.Networks "kind").IPAddress}}'],text=True).strip()
    until=time.monotonic()+60
    while True:
        reply=subprocess.run(['curl','--silent','--show-error','--max-time','5','--cacert',str(state/'ca.crt'),
                              '--connect-to','worth.oopsbox.cn:443:'+node+':'+str(port),
                              '--write-out','\n%{http_code}','https://worth.oopsbox.cn/api/tasks/'+task],text=True,capture_output=True)
        if reply.returncode==0 and reply.stdout.endswith('\n200'):break
        if time.monotonic()>=until:raise RuntimeError('Real v6 baseline not publicly readable')
        time.sleep(1)
    drained=False
    injected=False
    def upgrading_kube(*args,**kwargs):
        nonlocal drained,injected
        if args[0]=='apply' and kwargs.get('input'):
            raw=kwargs['input']
            if not raw.lstrip().startswith('{'):
                raw=kube('create','--dry-run=client','--validate=false','-f','-','-o','json',input=raw,capture=True).stdout
            for obj in objects(raw):
                if obj['kind']=='Deployment' and obj['metadata']['name'] in ('content','voting') and not drained:
                    raise RuntimeError('New protocol writer started before real v6 Pod drain')
        result=kube(*args,**kwargs)
        if '--for=delete' in args and 'app=content' in args:
            remaining=json.loads(kube('-n','human-worth','get','pods','-l','app=content','-o','json',capture=True).stdout)['items']
            if remaining:raise RuntimeError('Old Content Pods survived drain')
            drained=True
        if 'rollout' in args and 'deployment/voting' in args and not injected:
            injected=True
            sql("CREATE FUNCTION voting.upgrade_fail_activate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated upgrade activation fault'; END $$; CREATE TRIGGER upgrade_fail_activate BEFORE UPDATE ON voting.catalog_gates FOR EACH ROW WHEN (NEW.state <> 'frozen') EXECUTE FUNCTION voting.upgrade_fail_activate();")
            kube('-n','human-worth','scale','deployment/voting','--replicas=0')
            kube('-n','human-worth','wait','--for=delete','pod','-l','app=voting','--timeout=90s')
        return result
    try:
        with patch.object(app,'kube',upgrading_kube):app.main(revision=revision)
    except (subprocess.CalledProcessError,RuntimeError):
        if not drained or not injected:raise
    else:raise RuntimeError('Voting outage unexpectedly permitted bootstrap completion')
    def wait_stage(stage):
        deadline=time.monotonic()+90
        while sql("SELECT stage FROM content.voting_catalog_changes WHERE task_id='"+task+"'")!=stage:
            if time.monotonic()>=deadline:raise RuntimeError('Upgrade did not persist '+stage)
            time.sleep(1)
    def rejected_rollback(stage):
        wait_stage(stage)
        before=kube('-n','human-worth','get','deployments','content','voting','-o','json',capture=True).stdout
        try:release.restore(previous,None)
        except RuntimeError as error:
            if 'rollback refused' not in str(error):raise
        else:raise RuntimeError('Real v6 rollback accepted after '+stage)
        after=kube('-n','human-worth','get','deployments','content','voting','-o','json',capture=True).stdout
        specs=lambda raw:{p['metadata']['name']:p['spec'] for p in json.loads(raw)['items']}
        if specs(before)!=specs(after):raise RuntimeError('Refused rollback changed live Content/Voting templates')
        pods=json.loads(kube('-n','human-worth','get','pods','-l','app=content','-o','json',capture=True).stdout)['items']
        if len(pods)!=2 or any(p['spec']['containers'][0]['image']!= 'human-worth/content:'+revision for p in pods):
            raise RuntimeError('Refused rollback lost new Content replicas')
        if sql("SELECT count(*) FROM content.voting_catalog_heads WHERE task_id='"+task+"' AND active_change IS NOT NULL")!='1':
            raise RuntimeError('Refused rollback lost durable intent')
    rejected_rollback('queued')
    kube('-n','human-worth','scale','deployment/voting','--replicas=2')
    kube('-n','human-worth','rollout','status','deployment/voting','--timeout=180s')
    rejected_rollback('applied')
    if sql("SELECT state FROM voting.catalog_gates WHERE task_id='"+task+"'")!='frozen':raise RuntimeError('Applied fixture lost Voting freeze')
    sql('DROP TRIGGER upgrade_fail_activate ON voting.catalog_gates; DROP FUNCTION voting.upgrade_fail_activate()')
    wait_stage('completed')
    app.main(revision=revision)
    print('PASS: real a155639 v6 Pods and historical SQL fixture; old Pods deleted before new writers; queued/applied persist and reject v6 rollback; forward bootstrap recovery',flush=True)


def main(cluster='lab', kubeconfig=None):
    if cluster not in ('lab','human-worth-backend-check'):raise ValueError('Only isolated lab clusters are accepted')
    config=Path(kubeconfig) if kubeconfig else infra.KUBECONFIG
    if shutil.which('kubectl') is None or shutil.which('kind') is None or not config.is_file():
        raise RuntimeError('INCONCLUSIVE: kubectl, kind and the isolated lab kubeconfig are required')
    namespace='human-worth-verify-'+secrets.token_hex(4)
    def original(*args, **kwargs):
        return infra.run('kubectl','--kubeconfig',str(config),'--context','kind-'+cluster,*args,**kwargs)
    def run(*args, **kwargs):
        args=list(args)
        if args[0]=='kind' and '--name' in args:args[args.index('--name')+1]=cluster
        return infra.run(*args,**kwargs)
    port=18444
    checks=[check for spec in app.load_services(app.REPO).values() for check in spec['checks']]

    def rewrite(value):
        if isinstance(value, str):
            if value=='human-worth':return namespace
            return value.replace('.human-worth.svc.cluster.local','.'+namespace+'.svc.cluster.local').replace('human-worth-execution',namespace+'-execution').replace('challenge-worker-runtime',namespace+'-worker-runtime')
        if isinstance(value, list): return [rewrite(v) for v in value]
        if not isinstance(value, dict): return value
        value={k:rewrite(v) for k,v in value.items()}
        if value.get('namespace')=='human-worth':value['namespace']=namespace
        if value.get('kind')=='Service' and value['spec'].get('type')=='NodePort':
            # Keep real Service endpoint selection; do not reuse public 30443.
            for item in value['spec']['ports']:item.pop('nodePort',None)
        if value.get('kind')=='Cluster':
            value['spec']['instances']=1
            value['spec']['postgresql'].pop('synchronous',None)
            value['spec']['storage']['size']='512Mi'
            if cluster=='human-worth-backend-check':
                value['spec']['postgresql']['pg_hba']=[v.replace('10.244.0.0/16','10.245.0.0/16') for v in value['spec']['postgresql']['pg_hba']]
        if value.get('kind')=='Secret':
            data={}
            for key, encoded in value['data'].items():
                raw=base64.b64decode(encoded).decode()
                try:
                    decoded=json.loads(raw)
                    raw=json.dumps(rewrite(decoded)) if isinstance(decoded,(dict,list)) else rewrite(raw)
                except ValueError:raw=rewrite(raw)
                data[key]=base64.b64encode(raw.encode()).decode()
            value['data']=data
        return value

    def kube(*args,input=None,capture=False):
        args=list(args)
        for i,arg in enumerate(args):
            if i and args[i-1] in ('-n','--namespace') and arg=='human-worth':args[i]=namespace
        if input and '--dry-run=client' not in args:
            # Only Kubernetes manifests are transformed; SQL and passwords are opaque.
            if args[0] in ('apply','create'):
                decoded=objects(original('create','--dry-run=client','--validate=false','-f','-','-o','json',input=input,capture=True).stdout)
                input=json.dumps({'apiVersion':'v1','kind':'List','items':[rewrite(obj) for obj in decoded]})
        return original(*args,input=input,capture=capture)

    with tempfile.TemporaryDirectory(prefix='human-worth-module-validation-') as directory:
        state=Path(directory)/'private';state.mkdir(mode=0o700)
        # Agent Self-Claimed: synthetic OAuth configuration exercises only URL
        # construction/login-flow persistence, never Google exchange or real keys.
        oauth=state/'test-google-oauth.json'
        oauth.write_text(json.dumps({'client_id':'module-validation.apps.googleusercontent.com','client_secret':secrets.token_urlsafe(32)}))
        oauth.chmod(0o600)
        source=Path(directory)/'source'
        # Keep one immutable test input even if the developer continues editing.
        paths=subprocess.check_output(['git','ls-files','--cached','--others','--exclude-standard','-z'],cwd=app.REPO).decode().split('\0')
        for path in filter(None,paths):
            if (app.REPO/path).is_file():
                target=source/path;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(app.REPO/path,target)
        legacy_revision=subprocess.check_output(['git','rev-parse','a155639^{commit}'],cwd=app.REPO,text=True).strip()
        legacy_source=Path(directory)/'v6';legacy_source.mkdir()
        release.extract_release(subprocess.check_output(['git','archive',legacy_revision],cwd=app.REPO),legacy_source)
        parent=infra.STATE/'egress-parent.json'
        if parent.exists():shutil.copyfile(parent,state/'egress-parent.json')
        curl_options=['--cacert',str(state/'ca.crt'),'--connect-to','worth.oopsbox.cn:443:127.0.0.1:'+str(port)]
        forward=None
        print('Disposable namespace: '+namespace,flush=True)
        try:
            kube('apply','--server-side','-f','-',input=(infra.HERE/'namespace.yaml').read_text())
            with patch.dict(os.environ,{'GOOGLE_OAUTH_FILE':str(oauth)}),patch.object(app,'REPO',source),patch.object(app,'STATE',state),patch.object(release,'STATE',state),patch.object(app,'KUBECONFIG',config),patch.object(app,'run',run),patch.object(app,'kube',kube),patch.object(infra,'kube',kube),patch.object(release,'kube',kube),patch.object(object_store,'apply',lambda text:kube('apply','--server-side','-f','-',input=text)),patch.object(object_store,'kube',kube),patch.object(object_store,'STATE',state),patch.object(object_store,'HERE',source/'ops/lab'):
                revision=hashlib.sha256((app.source_hash()+(source/'ops/lab/app.py').read_text()).encode()).hexdigest()[:40]
                modules=app.load_services(source)
                # app.main reconciles the private Asset store before Asset itself.
                catalog_upgrade_regression(kube,state,legacy_source,legacy_revision,revision,cluster)
                node_port=kube('-n',namespace,'get','service','gateway','-o','jsonpath={.spec.ports[0].nodePort}',capture=True).stdout.strip()
                node_ip=subprocess.check_output(['docker','inspect',cluster+'-control-plane','--format','{{(index .NetworkSettings.Networks "kind").IPAddress}}'],text=True).strip()
                if not node_ip or not node_port.isdigit():raise RuntimeError('Private validation NodePort unavailable')
                with open(Path(directory)/'forward.log','w') as log:
                    forward=subprocess.Popen(['kubectl','--kubeconfig',str(config),'--context','kind-'+cluster,'-n',namespace,'port-forward','--address=127.0.0.1','service/gateway',str(port)+':443'],stdout=log,stderr=log)
                    deadline=time.monotonic()+45
                    while not healthy(revision,checks,curl_options):
                        if forward.poll() is not None or time.monotonic()>=deadline:raise RuntimeError('Isolated gateway did not become healthy')
                        time.sleep(1)
                    print('PASS: all module rollouts and HTTPS checks',flush=True)
                    if healthy(revision,checks,curl_options,modules):raise RuntimeError('Missing publication marker was accepted')
                    release.publish_status(revision,modules)
                    deadline=time.monotonic()+180
                    while not healthy(revision,checks,curl_options,modules):
                        if time.monotonic()>=deadline:raise RuntimeError('Completion ConfigMap did not reach the gateway')
                        time.sleep(2)
                    print('PASS: completion marker appears only after all modules pass',flush=True)
                    env={**os.environ,'HUMAN_WORTH_VERIFY_NAMESPACE':namespace,'HUMAN_WORTH_VERIFY_STATE':str(state),'HUMAN_WORTH_VERIFY_KUBECONFIG':str(config),'HUMAN_WORTH_VERIFY_CONTEXT':'kind-'+cluster,'HUMAN_WORTH_VERIFY_PORT':str(port),'HUMAN_WORTH_VERIFY_GATEWAY_ADDRESS':node_ip+':'+node_port}
                    subprocess.run(['go','test','-race','-tags=lab','./internal/identity','-run','^TestKind(Content|Asset|PublicAsset|Moderation|AdditionalEntry|Voting|FileAuthorizationRecovery)Deployment$|^TestKindRecovery','-count=1','-timeout=900s','-v'],cwd=source/'backend',env=env,check=True)
                    # A separate budget covers the deliberate 280s outage; the
                    # original suite and 90s endpoint recovery deadline stay intact.
                    subprocess.run(['go','test','-race','-tags=lab','./internal/identity','-run','^TestKindDependencyRecovery$','-count=1','-timeout=600s','-v'],cwd=source/'backend',env=env,check=True)
                    import check_network
                    with patch.object(check_network,'kube',kube),patch.object(check_network,'run',run):check_network.main()
                    previous=release.snapshot()
                    kube('-n','human-worth','patch','deployment','content','--type=json','-p',json.dumps([{'op':'replace','path':'/spec/template/spec/containers/0/image','value':'human-worth/content:missing-validation-image'}]))
                    try:
                        kube('-n','human-worth','rollout','status','deployment/content','--timeout=12s',capture=True)
                    except subprocess.CalledProcessError:pass
                    else:raise RuntimeError('Broken image unexpectedly rolled out')
                    release.publish_status('0'*40,modules)
                    release.restore(previous,None)
                    # Match the real reconciler: Pod readiness alone does not
                    # prove every headless gRPC caller has reconnected yet.
                    with patch.object(release,'health',lambda sha,checks=(),services=():healthy(sha,checks,curl_options,services)):
                        if not release.wait_health(revision,checks=checks):raise RuntimeError('Rollback did not recover HTTPS')
                    print('PASS: failed rollout restored all module templates and policies',flush=True)
                    # Corrupt only this temporary owner's DSN, then exercise same-SHA retry.
                    saved=json.loads(kube('-n','human-worth','get','secret','content-owner','-o','json',capture=True).stdout)
                    image='human-worth/content:'+revision
                    job='content-migrate-'+revision
                    kube('-n','human-worth','delete','job',job,'--wait=true')
                    app.secret('content-owner',{'database-url':'postgres://invalid@127.0.0.1:1/invalid?connect_timeout=1'})
                    try:
                        def quick_kube(*args,**kwargs):
                            return kube(*(arg.replace('--timeout=150s','--timeout=15s') for arg in args),**kwargs)
                        with patch.object(app,'kube',quick_kube):app.migration(image,'content')
                    except (subprocess.CalledProcessError, RuntimeError):pass
                    else:raise RuntimeError('Invalid migration credentials unexpectedly succeeded')
                    kube('-n','human-worth','patch','secret','content-owner','--type=merge','-p',json.dumps({'data':saved['data']}),capture=True)
                    app.migration(image,'content')
                    print('PASS: failed migration retried successfully with the same image revision',flush=True)
                    # Reconciliation must be idempotent and keep the dependency healthy.
                    app.main(revision=revision)
                    with patch.object(release,'health',lambda sha,checks=(),services=():healthy(sha,checks,curl_options,services)):
                        if not release.wait_health(revision,checks=checks):raise RuntimeError('Repeated deployment became unhealthy')
                    print('PASS: repeated complete module deployment',flush=True)
        finally:
            if forward is not None:
                forward.terminate();forward.wait(timeout=10)
            original('delete','namespace',namespace,'--wait=true','--timeout=180s',capture=True)
            original('delete','namespace',namespace+'-execution','--ignore-not-found','--wait=true','--timeout=180s',capture=True)
            for kind in ('clusterrolebinding','clusterrole'):
                original('delete',kind,namespace+'-worker-runtime','--ignore-not-found',capture=True)
            print('Cleaned disposable namespace: '+namespace,flush=True)


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--cluster',default='lab',choices=['lab','human-worth-backend-check'])
    parser.add_argument('--kubeconfig')
    args=parser.parse_args()
    main(args.cluster,args.kubeconfig)
