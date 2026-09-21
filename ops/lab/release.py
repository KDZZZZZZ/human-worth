#!/usr/bin/env python3
"""Reconcile only an immutable dev revision with successful push CI into kind-lab.

Install this file, app/infra helpers and manifests outside Git as reviewed, root-
owned operational code. Never run release scripts from the fetched repository.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

sys.path.insert(0,str(Path(__file__).resolve().parent.parent))
from deploy import REPO, ci_passed, command, extract_release, fetch_revision, read_json
from deployment_health import healthy
import app
from infra import STATE, kube
from services import NAMES, load_services

BASE=Path.home()/'.local/share/human-worth-identity'


def require_approved(sha):
    if not re.fullmatch('[a-f0-9]{40}',sha):raise ValueError('Invalid release SHA')
    current=command(['git','ls-remote','https://github.com/'+REPO+'.git','refs/heads/dev'],text=True).split()[0]
    if current!=sha:raise RuntimeError('Release is no longer the current dev revision')
    runs=read_json('https://api.github.com/repos/'+REPO+'/actions/workflows/ci.yml/runs?branch=dev&event=push&head_sha='+sha+'&per_page=100')
    if not ci_passed(runs.get('workflow_runs',[]),sha):raise RuntimeError('Waiting for successful matching dev push CI')


def health(revision, checks=(), services=()):
    return healthy(revision, checks, ['--cacert',str(STATE/'ca.crt'),'--connect-to','worth.oopsbox.cn:443:127.0.0.1:18443'], services)


def wait_health(revision,timeout=45,checks=(),services=()):
    # Pod readiness can precede headless DNS/gRPC and NodePort convergence.
    deadline=time.monotonic()+timeout
    while True:
        if health(revision,checks,services):return True
        if time.monotonic()>=deadline:return False
        time.sleep(1)


def snapshot():
    items=json.loads(kube('-n','human-worth','get','deployments,networkpolicies,configmaps','-o','json',capture=True).stdout)['items']
    return [{'apiVersion':p['apiVersion'],'kind':p['kind'],'metadata':{'name':p['metadata']['name'],'namespace':'human-worth','generation':p['metadata'].get('generation')},**({'data':p.get('data',{})} if p['kind']=='ConfigMap' else {'spec':p['spec']}),'status':p.get('status',{})} for p in items if (p['kind']=='ConfigMap' and p['metadata']['name']=='release-status') or (p['kind']!='ConfigMap' and p['metadata']['name'] in NAMES)]


def publish_status(sha, services):
    marker={'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':'release-status','namespace':'human-worth'},
            'data':{'status.json':json.dumps({'revision':sha,'services':list(services)})}}
    kube('apply','--server-side','--field-manager=human-worth-release','-f','-',input=json.dumps(marker))


def restore(deployments,previous_build):
    # Restore former pod templates and Secret references; do not undo SQL migrations.
    for deployment in deployments:
        field='data' if deployment.get('kind')=='ConfigMap' else 'spec'
        kube('-n','human-worth','patch',deployment.get('kind','Deployment').lower(),deployment['metadata']['name'],'--field-manager=human-worth-release','--type=json','-p',json.dumps([{'op':'replace','path':'/'+field,'value':deployment[field]}]))
    for deployment in deployments:
        if deployment.get('kind','Deployment')=='Deployment':
            kube('-n','human-worth','rollout','status','deployment/'+deployment['metadata']['name'],'--timeout=180s')
    if previous_build is not None:(STATE/'build.json').write_text(previous_build)


def reconcile(check_only=False):
    BASE.mkdir(parents=True,exist_ok=True)
    cache=BASE/'repository.git'
    sha=fetch_revision(cache)
    require_approved(sha)
    release=BASE/'releases'/sha
    if not release.exists():
        staging=release.with_name(sha+'.staging-'+str(os.getpid()))
        staging.mkdir(parents=True)
        extract_release(command(['git','--git-dir',str(cache),'archive',sha]),staging)
        (staging/'REVISION').write_text(sha+'\n')
        staging.rename(release)
    if not (release/'backend/cmd/identity/main.go').is_file():
        raise RuntimeError('Approved dev revision does not contain Identity yet; no activation')
    services=load_services(release)
    checks=[check for spec in services.values() for check in spec['checks']]
    if check_only:
        print('Eligible immutable application release: '+sha+' ('+', '.join(services)+')');return
    state_path=BASE/'state.json'
    state=json.loads(state_path.read_text()) if state_path.exists() else {}
    if state.get('failed_sha')==sha and time.time()<state.get('retry_after',0):return
    certs_current=all(subprocess.run(['openssl','x509','-noout','-checkend','86400','-in',str(STATE/(service+'.crt'))],capture_output=True).returncode==0 for service in services)
    current=snapshot()
    deployments={p['metadata']['name']:p for p in current if p['kind']=='Deployment'}
    if deployments.keys()-services.keys():raise RuntimeError('Module removal needs an explicit retirement operation')
    ready=all(name in deployments
              and deployments[name]['spec']['template']['spec']['containers'][0]['image']=='human-worth/'+name+':'+sha
              and deployments[name]['status'].get('observedGeneration')==deployments[name]['metadata'].get('generation')
              and deployments[name]['status'].get('availableReplicas',0)==deployments[name]['spec']['replicas']
              and deployments[name]['status'].get('updatedReplicas',0)==deployments[name]['spec']['replicas']
              and deployments[name]['spec']['replicas']>=2 for name in services)
    if state.get('deployed_sha')==sha and certs_current and ready and health(sha,checks,services):
        print('Already deployed '+sha);return
    previous=current
    previous_build=(STATE/'build.json').read_text() if (STATE/'build.json').exists() else None
    app.REPO=release
    try:
        app.main(revision=sha,before_activation=lambda:require_approved(sha))
        if not wait_health(sha,checks=checks):raise RuntimeError('Activated module health or revision mismatch')
        require_approved(sha)
        publish_status(sha,services)
        if not wait_health(sha,timeout=180,checks=checks,services=services):raise RuntimeError('Deployment completion marker did not reach the gateway')
    except Exception:
        # A first activation of a new module must not leave its failed workload running.
        current_names={p['metadata']['name'] for p in snapshot() if p['kind']=='Deployment'}
        for name in current_names-deployments.keys():
            kube('-n','human-worth','scale','deployment/'+name,'--replicas=0',capture=True)
        if not any(p['kind']=='ConfigMap' for p in previous):
            kube('-n','human-worth','delete','configmap','release-status','--ignore-not-found',capture=True)
        if previous:
            restore(previous,previous_build)
            if 'gateway' in deployments:
                old_revision=deployments['gateway']['spec']['template']['spec']['containers'][0]['image'].rsplit(':',1)[1]
                if not wait_health(old_revision):raise RuntimeError('Release failed and previous revision is not healthy') from None
        state.update(failed_sha=sha,retry_after=time.time()+180)
        state_path.write_text(json.dumps(state,indent=2)+'\n')
        raise RuntimeError('Application release failed; former application templates restored, database migrations retained') from None
    state.update(deployed_sha=sha,services=list(services),failed_sha=None,retry_after=0,deployed_at=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()))
    tmp=state_path.with_suffix('.tmp');tmp.write_text(json.dumps(state,indent=2)+'\n');tmp.replace(state_path)
    (STATE/'release-revision').write_text(sha+'\n')
    print('Deployed '+', '.join(services)+' at '+sha+'; HTTPS health passed',flush=True)


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--check-only',action='store_true');options=parser.parse_args()
    BASE.mkdir(parents=True,exist_ok=True)
    with (BASE/'deploy.lock').open('w') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        reconcile(options.check_only)
