#!/usr/bin/env python3
"""Exercise module deployment in a disposable namespace of the existing kind lab.

Uses the real manifests/controller, an isolated single-instance PostgreSQL and
ClusterIP gateway. Never changes the public namespace, NodePort or its secrets.
"""
import base64
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


def main():
    namespace='human-worth-verify-'+secrets.token_hex(4)
    original=infra.kube
    port=18444
    checks=[check for spec in app.load_services(app.REPO).values() for check in spec['checks']]

    def rewrite(value):
        if isinstance(value, str): return value.replace('.human-worth.svc.cluster.local','.'+namespace+'.svc.cluster.local')
        if isinstance(value, list): return [rewrite(v) for v in value]
        if not isinstance(value, dict): return value
        value={k:rewrite(v) for k,v in value.items()}
        if value.get('namespace')=='human-worth':value['namespace']=namespace
        if value.get('kind')=='Namespace':value['metadata']['name']=namespace
        if value.get('kind')=='Service' and value['spec'].get('type')=='NodePort':
            value['spec']['type']='ClusterIP'
            for item in value['spec']['ports']:item.pop('nodePort',None)
        if value.get('kind')=='Cluster':
            value['spec']['instances']=1
            value['spec']['postgresql'].pop('synchronous',None)
            value['spec']['storage']['size']='512Mi'
        if value.get('kind')=='Secret':
            value['data']={k:base64.b64encode(rewrite(base64.b64decode(v).decode()).encode()).decode() for k,v in value['data'].items()}
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
        source=Path(directory)/'source'
        # Keep one immutable test input even if the developer continues editing.
        paths=subprocess.check_output(['git','ls-files','--cached','--others','--exclude-standard','-z'],cwd=app.REPO).decode().split('\0')
        for path in filter(None,paths):
            if (app.REPO/path).is_file():
                target=source/path;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(app.REPO/path,target)
        parent=infra.STATE/'egress-parent.json'
        if parent.exists():shutil.copyfile(parent,state/'egress-parent.json')
        curl_options=['--cacert',str(state/'ca.crt'),'--connect-to','worth.oopsbox.cn:443:127.0.0.1:'+str(port)]
        forward=None
        print('Disposable namespace: '+namespace,flush=True)
        try:
            kube('apply','--server-side','-f','-',input=(infra.HERE/'namespace.yaml').read_text())
            with patch.object(app,'REPO',source),patch.object(app,'STATE',state),patch.object(app,'kube',kube),patch.object(infra,'kube',kube),patch.object(release,'kube',kube):
                revision=hashlib.sha256((app.source_hash()+(source/'ops/lab/app.py').read_text()).encode()).hexdigest()[:40]
                modules=app.load_services(source)
                app.main(revision=revision)
                with open(Path(directory)/'forward.log','w') as log:
                    forward=subprocess.Popen(['kubectl','--kubeconfig',str(infra.KUBECONFIG),'--context','kind-lab','-n',namespace,'port-forward','--address=127.0.0.1','service/gateway',str(port)+':443'],stdout=log,stderr=log)
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
                    env={**os.environ,'HUMAN_WORTH_VERIFY_NAMESPACE':namespace,'HUMAN_WORTH_VERIFY_STATE':str(state),'HUMAN_WORTH_VERIFY_KUBECONFIG':str(infra.KUBECONFIG),'HUMAN_WORTH_VERIFY_PORT':str(port)}
                    subprocess.run(['go','test','-race','-tags=lab','./internal/identity','-run','^TestKindContentDeployment$','-count=1','-timeout=240s','-v'],cwd=app.REPO/'backend',env=env,check=True)
                    import check_network
                    with patch.object(check_network,'kube',kube):check_network.main()
                    previous=release.snapshot()
                    kube('-n','human-worth','patch','deployment','content','--type=json','-p',json.dumps([{'op':'replace','path':'/spec/template/spec/containers/0/image','value':'human-worth/content:missing-validation-image'}]))
                    try:
                        kube('-n','human-worth','rollout','status','deployment/content','--timeout=12s',capture=True)
                    except subprocess.CalledProcessError:pass
                    else:raise RuntimeError('Broken image unexpectedly rolled out')
                    release.publish_status('0'*40,modules)
                    release.restore(previous,None)
                    if not healthy(revision,checks,curl_options):raise RuntimeError('Rollback did not recover HTTPS')
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
                    if not healthy(revision,checks,curl_options):raise RuntimeError('Repeated deployment became unhealthy')
                    print('PASS: repeated complete module deployment',flush=True)
        finally:
            if forward is not None:
                forward.terminate();forward.wait(timeout=10)
            original('delete','namespace',namespace,'--wait=true','--timeout=180s',capture=True)
            print('Cleaned disposable namespace: '+namespace,flush=True)


if __name__=='__main__':main()
