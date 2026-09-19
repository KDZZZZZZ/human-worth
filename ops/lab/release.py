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
import app
from infra import STATE, kube

BASE=Path.home()/'.local/share/human-worth-identity'


def require_approved(sha):
    if not re.fullmatch('[a-f0-9]{40}',sha):raise ValueError('Invalid release SHA')
    current=command(['git','ls-remote','https://github.com/'+REPO+'.git','refs/heads/dev'],text=True).split()[0]
    if current!=sha:raise RuntimeError('Release is no longer the current dev revision')
    runs=read_json('https://api.github.com/repos/'+REPO+'/actions/workflows/ci.yml/runs?branch=dev&event=push&head_sha='+sha+'&per_page=100')
    if not ci_passed(runs.get('workflow_runs',[]),sha):raise RuntimeError('Waiting for successful matching dev push CI')


def health(revision):
    try:
        data=command(['curl','--fail','--silent','--show-error','--noproxy','*','--max-time','5','--cacert',str(STATE/'ca.crt'),'--connect-to','worth.oopsbox.cn:443:127.0.0.1:18443','https://worth.oopsbox.cn/api/health'],text=True)
        result=json.loads(data)
        return result.get('revision')==revision and result.get('status')=='ok' and result.get('stage')=='identity'
    except (subprocess.SubprocessError,ValueError):return False


def snapshot():
    items=json.loads(kube('-n','human-worth','get','deployments','identity','gateway','--ignore-not-found','-o','json',capture=True).stdout)['items']
    return [{'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':p['metadata']['name'],'namespace':'human-worth'},'spec':p['spec']} for p in items]


def restore(deployments,previous_build):
    # Restore former pod templates and Secret references; do not undo SQL migrations.
    for deployment in deployments:
        kube('-n','human-worth','patch','deployment',deployment['metadata']['name'],'--type=json','-p',json.dumps([{'op':'replace','path':'/spec','value':deployment['spec']}]))
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
    if check_only:
        print('Eligible immutable Identity release: '+sha);return
    state_path=BASE/'state.json'
    state=json.loads(state_path.read_text()) if state_path.exists() else {}
    if state.get('failed_sha')==sha and time.time()<state.get('retry_after',0):return
    certs_current=all(subprocess.run(['openssl','x509','-noout','-checkend','86400','-in',str(STATE/(service+'.crt'))],capture_output=True).returncode==0 for service in ['identity','gateway'])
    if state.get('deployed_sha')==sha and certs_current and health(sha):
        print('Already deployed '+sha);return
    previous=snapshot()
    previous_build=(STATE/'build.json').read_text() if (STATE/'build.json').exists() else None
    app.REPO=release
    try:
        app.main(revision=sha,before_activation=lambda:require_approved(sha))
        if not health(sha):raise RuntimeError('Activated Identity health revision mismatch')
    except Exception:
        if previous:
            restore(previous,previous_build)
            old_revision=previous[0]['spec']['template']['spec']['containers'][0]['image'].rsplit(':',1)[1]
            if not health(old_revision):raise RuntimeError('Release failed and previous revision is not healthy') from None
        else:
            # No previously healthy deployment: keep a partial first release out of service.
            kube('-n','human-worth','scale','deployment/identity','deployment/gateway','--replicas=0',capture=True)
        state.update(failed_sha=sha,retry_after=time.time()+180)
        state_path.write_text(json.dumps(state,indent=2)+'\n')
        raise RuntimeError('Identity release failed; former application templates restored, database migrations retained') from None
    state.update(deployed_sha=sha,failed_sha=None,retry_after=0,deployed_at=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()))
    tmp=state_path.with_suffix('.tmp');tmp.write_text(json.dumps(state,indent=2)+'\n');tmp.replace(state_path)
    (STATE/'release-revision').write_text(sha+'\n')
    print('Deployed Identity '+sha+'; HTTPS health passed',flush=True)


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--check-only',action='store_true');options=parser.parse_args()
    BASE.mkdir(parents=True,exist_ok=True)
    with (BASE/'deploy.lock').open('w') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        reconcile(options.check_only)
