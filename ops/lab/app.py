#!/usr/bin/env python3
"""Build and deploy the Identity slice into kind-lab. Secrets stay outside Git."""
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess

from infra import HERE, STATE, KUBECONFIG, apply, kube, pin_images, run

REPO = HERE.parent.parent


def private_file(path, value):
    if not path.exists():
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as f:
            f.write(value)
    path.chmod(0o600)
    return path.read_text().strip()


def secret(name, values, kind='Opaque'):
    obj = {'apiVersion':'v1','kind':'Secret','metadata':{'name':name,'namespace':'human-worth'},'type':kind,
           'data':{k:base64.b64encode(v.encode()).decode() for k,v in values.items()}}
    # Never let a validation error echo a secret manifest into logs.
    try:
        kube('apply', '--server-side', '-f', '-', input=json.dumps(obj), capture=True)
    except subprocess.CalledProcessError:
        raise RuntimeError('Secret installation failed: '+name) from None


def certificates():
    ca_key, ca_cert = STATE/'ca.key', STATE/'ca.crt'
    if not ca_key.exists():
        run('openssl','req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-keyout',str(ca_key),'-out',str(ca_cert),'-days','365','-subj','/CN=Human Worth lab CA',capture=True)
        ca_key.chmod(0o600)
    if subprocess.run(['openssl','x509','-checkend','86400','-noout','-in',str(ca_cert)],capture_output=True).returncode:
        raise RuntimeError('Lab CA expires soon; rotate CA with an overlap before deploying')
    for name in ['identity','gateway']:
        key, cert, csr, ext = (STATE/(name+suffix) for suffix in ['.key','.crt','.csr','.ext'])
        if not cert.exists() or subprocess.run(['openssl','x509','-checkend','86400','-noout','-in',str(cert)],capture_output=True).returncode:
            run('openssl','req','-new','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-keyout',str(key),'-out',str(csr),'-subj','/CN='+name,capture=True)
            key.chmod(0o600)
            ext.write_text('basicConstraints=CA:FALSE\nkeyUsage=digitalSignature\nextendedKeyUsage=serverAuth,clientAuth\nsubjectAltName=DNS:'+name+',DNS:worth.oopsbox.cn,URI:spiffe://human-worth/services/'+name+'\n')
            run('openssl','x509','-req','-in',str(csr),'-CA',str(ca_cert),'-CAkey',str(ca_key),'-CAcreateserial','-out',str(cert),'-days','30','-extfile',str(ext),capture=True)
        secret(name+'-tls',{'tls.crt':cert.read_text(),'tls.key':key.read_text(),'ca.crt':ca_cert.read_text()})


def source_hash():
    h=hashlib.sha256()
    for path in sorted((REPO/'backend').rglob('*')):
        if path.is_file() and not path.name.endswith('_test.go') and (path.parts[-1] in ['go.mod','go.sum','Dockerfile'] or any(part in ['cmd','internal','gen'] for part in path.relative_to(REPO/'backend').parts)):
            h.update(str(path.relative_to(REPO)).encode());h.update(path.read_bytes())
    h.update((REPO/'public/brand/logo.png').read_bytes())
    h.update((REPO/'.dockerignore').read_bytes())
    return h.hexdigest()


def build_images(revision=None):
    digest=source_hash()
    version=revision or 'local-'+digest[:16]
    result={'sourceHash':digest,'baseRevision':revision or run('git','-C',str(REPO),'rev-parse','HEAD',capture=True).stdout.strip(),'images':{}}
    for name in ['identity','gateway','identity-admin']:
        image='human-worth/'+name+':'+version
        run('docker','build','--provenance=false','--sbom=false','--network=host','--build-arg','HTTP_PROXY','--build-arg','HTTPS_PROXY','--build-arg','SERVICE='+name,'--build-arg','REVISION='+version,'-f',str(REPO/'backend/Dockerfile'),'-t',image,str(REPO))
        run('kind','load','docker-image','--name','lab',image)
        result['images'][name]={'tag':image,'id':run('docker','image','inspect',image,'--format','{{.Id}}',capture=True).stdout.strip()}
    if source_hash()!=digest: raise RuntimeError('Source changed while building; rerun after edits finish')
    (STATE/'build.json').write_text(json.dumps(result,indent=2)+'\n')
    return result


def database_secrets():
    for role in ['owner','runtime','operator']:
        password=private_file(STATE/('identity-'+role+'.password'),secrets.token_urlsafe(32))
        secret('identity-'+role+'-password',{'username':'identity_'+role,'password':password},'kubernetes.io/basic-auth')
        values={'database-url':'postgres://identity_'+role+':'+password+'@database-rw.human-worth.svc.cluster.local:5432/human_worth?sslmode=verify-full&sslrootcert=/database-ca/ca.crt'}
        if role=='runtime':
            for purpose in ['encryption','signing']:
                ring=json.dumps({'active':'key1','keys':{'key1':base64.b64encode(secrets.token_bytes(32)).decode()}})
                values[purpose+'-keys.json']=private_file(STATE/(purpose+'-keys.json'),ring)
        secret('identity-'+role,values)


def maintenance_job(name, app, image, args, database, env=()):
    volume={'name':'database-ca','secret':{'secretName':'database-ca','defaultMode':0o440,'items':[{'key':'ca.crt','path':'ca.crt'}]}}
    pod={'restartPolicy':'Never','automountServiceAccountToken':False,'nodeSelector':{'human-worth.io/worker':'true'},
         'securityContext':{'runAsNonRoot':True,'runAsUser':65532,'runAsGroup':65532,'fsGroup':65532,'seccompProfile':{'type':'RuntimeDefault'}},
         'containers':[{'name':app,'image':image,'imagePullPolicy':'Never','args':args,
           'securityContext':{'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}},
           'resources':{'requests':{'cpu':'50m','memory':'64Mi'},'limits':{'cpu':'400m','memory':'128Mi'}},
           'env':[{'name':'IDENTITY_DATABASE_URL_FILE','value':'/database/database-url'},*env],
           'volumeMounts':[{'name':'database','mountPath':'/database','readOnly':True},{'name':'database-ca','mountPath':'/database-ca','readOnly':True}]}],
         'volumes':[{'name':'database','secret':{'secretName':database,'defaultMode':0o440}},volume]}
    return {'apiVersion':'batch/v1','kind':'Job','metadata':{'name':name,'namespace':'human-worth'},'spec':{'backoffLimit':0,'activeDeadlineSeconds':120,'ttlSecondsAfterFinished':86400,'template':{'metadata':{'labels':{'app':app}},'spec':pod}}}


def migration(image):
    name='identity-migrate-'+image.rsplit(':',1)[1]
    obj=maintenance_job(name,'identity-migrate',image,['migrate'],'identity-owner')
    policy={'apiVersion':'networking.k8s.io/v1','kind':'NetworkPolicy','metadata':{'name':'identity-maintenance','namespace':'human-worth'},'spec':{
        'podSelector':{'matchExpressions':[{'key':'app','operator':'In','values':['identity-migrate','identity-admin']}]},'policyTypes':['Egress'],
        'egress':[{'to':[{'podSelector':{'matchLabels':{'cnpg.io/cluster':'database'}}}],'ports':[{'protocol':'TCP','port':5432}]}]}}
    apply(json.dumps(policy));apply(json.dumps(obj))
    kube('-n','human-worth','wait','--for=condition=Complete','job/'+name,'--timeout=150s')


def main(revision=None, before_activation=None):
    STATE.mkdir(parents=True,mode=0o700,exist_ok=True);STATE.chmod(0o700)
    if revision is None and (STATE/'release-revision').exists():
        raise RuntimeError('This lab serves approved releases; local working-tree activation is disabled')
    if REPO in STATE.resolve().parents: raise RuntimeError('Lab secrets must be outside the repository')
    if not KUBECONFIG.exists(): raise RuntimeError('Run infra.py first')
    database_secrets()
    apply(pin_images((HERE/'postgres.yaml').read_text()))
    kube('-n','human-worth','wait','--for=condition=Ready','cluster/database','--timeout=900s')
    cluster=json.loads(kube('-n','human-worth','get','cluster','database','-o','json',capture=True).stdout)
    primary=cluster['status']['currentPrimary']
    kube('-n','human-worth','exec',primary,'--','psql','-X','-U','postgres','-d','human_worth','-v','ON_ERROR_STOP=1','-c','CREATE SCHEMA IF NOT EXISTS identity AUTHORIZATION identity_owner')
    certificates()
    google=Path(os.environ.get('GOOGLE_OAUTH_FILE',str(STATE.parent/'google-oauth.json')))
    oauth_version='google-disabled'
    if google.exists():
        cfg=json.loads(google.read_text());cfg=cfg.get('web',cfg)
        if not cfg.get('client_id') or not cfg.get('client_secret'): raise RuntimeError('Missing Google client configuration')
        callback='https://worth.oopsbox.cn/api/auth/google/callback'
        if 'redirect_uris' in cfg and callback not in cfg['redirect_uris']: raise RuntimeError('Google client JSON does not list the configured callback')
        oauth_version='google-'+hashlib.sha256(json.dumps([cfg['client_id'],cfg['client_secret'],callback]).encode()).hexdigest()[:16]
        secret('google-oauth',{'client-id':cfg['client_id'],'client-secret':cfg['client_secret']})
    images=build_images(revision)
    if before_activation: before_activation()
    migration(images['images']['identity']['tag'])
    kube('-n','human-worth','exec','-i',primary,'--','psql','-X','-U','postgres','-d','human_worth','-v','ON_ERROR_STOP=1',input=(HERE/'identity-grants.sql').read_text())
    egress=(HERE/'egress.yaml').read_text()
    parent=STATE/'egress-parent.json'
    if parent.exists():
        peer=json.loads(parent.read_text())
        egress=egress.replace('    cache deny all','    cache_peer '+peer['host']+' parent '+str(peer['port'])+' 0 no-query default\n    never_direct allow all\n    cache deny all')
        # Only the designated parent proxy is reachable from this egress Pod.
        begin=egress.index('  egress:\n')
        egress=egress[:begin]+'  egress:\n    - to: [{ipBlock: {cidr: '+peer['host']+'/32}}]\n      ports: [{protocol: TCP, port: '+str(peer['port'])+'}]\n'
    egress_hash=hashlib.sha256(egress.encode()).hexdigest()
    egress=egress.replace('metadata: {labels: {app: google-egress}}','metadata: {labels: {app: google-egress}, annotations: {human-worth.io/config: "'+egress_hash+'"}}')
    apply(egress)
    kube('-n','human-worth','rollout','status','deployment/google-egress','--timeout=180s')
    for name in ['identity','gateway']:
        text=(HERE/(name+'.yaml')).read_text().replace('human-worth/'+name+':local',images['images'][name]['tag'])
        # Environment and TLS credentials are read on process startup; secret changes require a rollout.
        fingerprint=hashlib.sha256((STATE/(name+'.crt')).read_bytes())
        if name=='identity':
            fingerprint.update((STATE/'identity-runtime.password').read_bytes())
            fingerprint.update((STATE/'encryption-keys.json').read_bytes());fingerprint.update((STATE/'signing-keys.json').read_bytes())
            if google.exists(): fingerprint.update(google.read_bytes())
        text=text.replace('metadata: {labels: {app: '+name+'}}','metadata: {labels: {app: '+name+'}, annotations: {human-worth.io/config: "'+fingerprint.hexdigest()+'"}}')
        if name=='identity': text=text.replace('value: google-v1','value: '+oauth_version)
        # These application manifests belong to the release controller. Reclaim
        # fields changed by an earlier rollback; do not force infrastructure fields.
        kube('apply','--server-side','--field-manager=human-worth-release','--force-conflicts','-f','-',input=text)
        kube('-n','human-worth','rollout','status','deployment/'+name,'--timeout=180s')
    print('Identity lab deployed; successful human Google login still requires public release and browser validation',flush=True)


if __name__=='__main__':
    main()
