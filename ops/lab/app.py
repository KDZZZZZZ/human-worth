#!/usr/bin/env python3
"""Deploy registered application modules into kind-lab. Secrets stay outside Git."""
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import time

from infra import HERE, STATE, KUBECONFIG, apply, kube, pin_images, run
from services import load_services, validate_manifest

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


def certificates(services):
    ca_key, ca_cert = STATE/'ca.key', STATE/'ca.crt'
    if not ca_key.exists():
        run('openssl','req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-keyout',str(ca_key),'-out',str(ca_cert),'-days','365','-subj','/CN=Human Worth lab CA',capture=True)
        ca_key.chmod(0o600)
    if subprocess.run(['openssl','x509','-checkend','86400','-noout','-in',str(ca_cert)],capture_output=True).returncode:
        raise RuntimeError('Lab CA expires soon; rotate CA with an overlap before deploying')
    for name in services:
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


def build_images(revision=None, services=None):
    services = services or load_services(REPO)
    digest=source_hash()
    version=revision or 'local-'+digest[:16]
    result={'sourceHash':digest,'baseRevision':revision or run('git','-C',str(REPO),'rev-parse','HEAD',capture=True).stdout.strip(),'images':{}}
    for name in [*services, 'identity-admin']:
        image='human-worth/'+name+':'+version
        run('docker','build','--provenance=false','--sbom=false','--network=host','--build-arg','HTTP_PROXY','--build-arg','HTTPS_PROXY','--build-arg','SERVICE='+name,'--build-arg','REVISION='+version,'-f',str(REPO/'backend/Dockerfile'),'-t',image,str(REPO))
        run('kind','load','docker-image','--name','lab',image)
        result['images'][name]={'tag':image,'id':run('docker','image','inspect',image,'--format','{{.Id}}',capture=True).stdout.strip()}
    if source_hash()!=digest: raise RuntimeError('Source changed while building; rerun after edits finish')
    (STATE/'build.json').write_text(json.dumps(result,indent=2)+'\n')
    return result


def database_secrets(services):
    for name, spec in services.items():
        if not spec['database']: continue
        for role in (['owner','runtime','operator'] if name == 'identity' else ['owner','runtime']):
            password=private_file(STATE/(name+'-'+role+'.password'),secrets.token_urlsafe(32))
            username=name.replace('-', '_')+'_'+role
            secret(name+'-'+role+'-password',{'username':username,'password':password},'kubernetes.io/basic-auth')
            values={'database-url':'postgres://'+username+':'+password+'@database-rw.human-worth.svc.cluster.local:5432/human_worth?sslmode=verify-full&sslrootcert=/database-ca/ca.crt'}
            if name == 'identity' and role=='runtime':
                for purpose in ['encryption','signing']:
                    ring=json.dumps({'active':'key1','keys':{'key1':base64.b64encode(secrets.token_bytes(32)).decode()}})
                    values[purpose+'-keys.json']=private_file(STATE/(purpose+'-keys.json'),ring)
            secret(name+'-'+role,values)


def database_manifest(services):
    roles, hba, clients = [], [], []
    for name, spec in services.items():
        if name == 'identity' or not spec['database']: continue
        schema = name.replace('-', '_')
        for role in ['owner', 'runtime']:
            roles.append('      - name: '+schema+'_'+role+'\n        login: true\n        passwordSecret: {name: '+name+'-'+role+'-password}')
        hba.append('      - hostssl human_worth '+schema+'_owner,'+schema+'_runtime 10.244.0.0/16 scram-sha-256')
        for app in [name, name+'-migrate']:
            clients.append('        - podSelector: {matchLabels: {app: '+app+'}}')
    text = (HERE/'postgres.yaml').read_text()
    for marker, lines in [('      # module-database-roles', roles), ('      # module-database-hba', hba), ('        # module-database-clients', clients)]:
        text = text.replace(marker, '\n'.join(lines))
    return pin_images(text)


def database_primary(services):
    # Cluster Ready may precede the asynchronous creation of newly managed roles.
    roles = [name.replace('-', '_')+'_'+role for name, spec in services.items() if spec['database'] for role in ['owner','runtime']]
    query = 'SELECT count(*) FROM pg_roles WHERE rolname IN ('+','.join("'"+role+"'" for role in roles)+')'
    deadline = time.monotonic()+90
    while True:
        primary=kube('-n','human-worth','get','cluster','database','-o','jsonpath={.status.currentPrimary}',capture=True).stdout
        count=kube('-n','human-worth','exec',primary,'--','psql','-X','-U','postgres','-d','human_worth','-At','-v','ON_ERROR_STOP=1','-c',query,capture=True).stdout.strip()
        if count == str(len(roles)): return primary
        if time.monotonic() >= deadline: raise RuntimeError('Database module roles are not ready')
        time.sleep(2)


def grant_runtime(primary, name, spec):
    # Approved grant SQL runs as the module owner, never as the database admin.
    sql = (REPO/spec['grants']).read_text().replace('\\set ON_ERROR_STOP on', '')
    if '\\' in sql: raise ValueError('psql meta-commands are not allowed in module grants')
    password=(STATE/(name+'-owner.password')).read_text().strip()
    try:
        kube('-n','human-worth','exec','-i',primary,'--','sh','-c',
             'IFS= read -r PGPASSWORD; export PGPASSWORD; exec psql "$@"','psql',
             '-X','--no-password','-d','postgresql://'+name.replace('-', '_')+'_owner@127.0.0.1/human_worth?sslmode=require',
             '-v','ON_ERROR_STOP=1','-c',sql,input=password+'\n',capture=True)
    except subprocess.CalledProcessError:
        raise RuntimeError('Module runtime grants failed: '+name) from None


def maintenance_job(name, app, image, args, database, env=(), service='identity'):
    volume={'name':'database-ca','secret':{'secretName':'database-ca','defaultMode':0o440,'items':[{'key':'ca.crt','path':'ca.crt'}]}}
    pod={'restartPolicy':'Never','automountServiceAccountToken':False,'nodeSelector':{'human-worth.io/worker':'true'},
         'securityContext':{'runAsNonRoot':True,'runAsUser':65532,'runAsGroup':65532,'fsGroup':65532,'seccompProfile':{'type':'RuntimeDefault'}},
         'containers':[{'name':app,'image':image,'imagePullPolicy':'Never','args':args,
           'securityContext':{'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}},
           'resources':{'requests':{'cpu':'50m','memory':'64Mi'},'limits':{'cpu':'400m','memory':'128Mi'}},
           'env':[{'name':service.upper().replace('-', '_')+'_DATABASE_URL_FILE','value':'/database/database-url'},*env],
           'volumeMounts':[{'name':'database','mountPath':'/database','readOnly':True},{'name':'database-ca','mountPath':'/database-ca','readOnly':True}]}],
         'volumes':[{'name':'database','secret':{'secretName':database,'defaultMode':0o440}},volume]}
    return {'apiVersion':'batch/v1','kind':'Job','metadata':{'name':name,'namespace':'human-worth'},'spec':{'backoffLimit':0,'activeDeadlineSeconds':120,'ttlSecondsAfterFinished':86400,'template':{'metadata':{'labels':{'app':app}},'spec':pod}}}


def migration(image, service='identity'):
    name=service+'-migrate-'+image.rsplit(':',1)[1]
    obj=maintenance_job(name,service+'-migrate',image,['migrate'],service+'-owner',service=service)
    policy={'apiVersion':'networking.k8s.io/v1','kind':'NetworkPolicy','metadata':{'name':service+'-maintenance','namespace':'human-worth'},'spec':{
        'podSelector':{'matchExpressions':[{'key':'app','operator':'In','values':[service+'-migrate']+(['identity-admin'] if service=='identity' else [])}]},'policyTypes':['Egress'],
        'egress':[{'to':[{'podSelector':{'matchLabels':{'cnpg.io/cluster':'database'}}}],'ports':[{'protocol':'TCP','port':5432}]}]}}
    previous=kube('-n','human-worth','get','job',name,'--ignore-not-found','-o','json',capture=True).stdout
    if previous and any(c.get('type')=='Failed' and c.get('status')=='True' for c in json.loads(previous).get('status',{}).get('conditions',[])):
        kube('-n','human-worth','delete','job',name,'--wait=true')
    apply(json.dumps(policy));apply(json.dumps(obj))
    kube('-n','human-worth','wait','--for=condition=Complete','job/'+name,'--timeout=150s')


def manifests(name, image):
    text=(REPO/'ops/lab'/(name+'.yaml')).read_text().replace('human-worth/'+name+':local',image)
    # kubectl's native YAML decoder avoids adding a second YAML implementation.
    raw=kube('create','--dry-run=client','--validate=false','-f','-','-o','json',input=text,capture=True).stdout
    objects=[]
    decoder=json.JSONDecoder()
    while raw.strip():
        obj, end=decoder.raw_decode(raw.lstrip())
        objects.extend(obj['items'] if obj.get('kind')=='List' else [obj])
        raw=raw.lstrip()[end:]
    validate_manifest(objects,name,image)
    return objects


def main(revision=None, before_activation=None):
    STATE.mkdir(parents=True,mode=0o700,exist_ok=True);STATE.chmod(0o700)
    if revision is None and (STATE/'release-revision').exists():
        raise RuntimeError('This lab serves approved releases; local working-tree activation is disabled')
    if REPO in STATE.resolve().parents: raise RuntimeError('Lab secrets must be outside the repository')
    if not KUBECONFIG.exists(): raise RuntimeError('Run infra.py first')
    services=load_services(REPO)
    images=build_images(revision,services)
    workloads={name:manifests(name,images['images'][name]['tag']) for name in services}
    if before_activation: before_activation()
    database_secrets(services)
    apply(database_manifest(services))
    kube('-n','human-worth','wait','--for=condition=Ready','cluster/database','--timeout=900s')
    primary=database_primary(services)
    for name, spec in services.items():
        if not spec['database']: continue
        schema=name.replace('-', '_')
        kube('-n','human-worth','exec',primary,'--','psql','-X','-U','postgres','-d','human_worth','-v','ON_ERROR_STOP=1','-c',
             'CREATE SCHEMA IF NOT EXISTS '+schema+' AUTHORIZATION '+schema+'_owner; REVOKE ALL ON SCHEMA '+schema+' FROM PUBLIC')
    certificates(services)
    google=Path(os.environ.get('GOOGLE_OAUTH_FILE',str(STATE.parent/'google-oauth.json')))
    oauth_version='google-disabled'
    if google.exists():
        cfg=json.loads(google.read_text());cfg=cfg.get('web',cfg)
        if not cfg.get('client_id') or not cfg.get('client_secret'): raise RuntimeError('Missing Google client configuration')
        callback='https://worth.oopsbox.cn/api/auth/google/callback'
        if 'redirect_uris' in cfg and callback not in cfg['redirect_uris']: raise RuntimeError('Google client JSON does not list the configured callback')
        oauth_version='google-'+hashlib.sha256(json.dumps([cfg['client_id'],cfg['client_secret'],callback]).encode()).hexdigest()[:16]
        secret('google-oauth',{'client-id':cfg['client_id'],'client-secret':cfg['client_secret']})
    if before_activation: before_activation()
    for name, spec in services.items():
        if not spec['database']: continue
        migration(images['images'][name]['tag'],name)
        grant_runtime(primary,name,spec)
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
    # Install all peer policies before waiting for a newly added dependency.
    for objects in workloads.values():
        for obj in objects:
            if obj['kind'] != 'Deployment':
                kube('apply','--server-side','--field-manager=human-worth-release','--force-conflicts','-f','-',input=json.dumps(obj))
    for name, spec in services.items():
        obj=next(obj for obj in workloads[name] if obj['kind']=='Deployment')
        # Environment and TLS credentials are read on process startup; secret changes require a rollout.
        fingerprint=hashlib.sha256((STATE/(name+'.crt')).read_bytes())
        if spec['database']: fingerprint.update((STATE/(name+'-runtime.password')).read_bytes())
        if name=='identity':
            fingerprint.update((STATE/'encryption-keys.json').read_bytes());fingerprint.update((STATE/'signing-keys.json').read_bytes())
            if google.exists(): fingerprint.update(google.read_bytes())
        obj['spec']['template']['metadata'].setdefault('annotations',{})['human-worth.io/config']=fingerprint.hexdigest()
        if name=='identity':
            for env in obj['spec']['template']['spec']['containers'][0]['env']:
                if env['name']=='GOOGLE_OAUTH_CONFIG_VERSION':env['value']=oauth_version
        # These application manifests belong to the release controller. Reclaim
        # fields changed by an earlier rollback; do not force infrastructure fields.
        kube('apply','--server-side','--field-manager=human-worth-release','--force-conflicts','-f','-',input=json.dumps(obj))
        kube('-n','human-worth','rollout','status','deployment/'+name,'--timeout=180s')
    print('Application modules deployed: '+', '.join(services),flush=True)


if __name__=='__main__':
    main()
