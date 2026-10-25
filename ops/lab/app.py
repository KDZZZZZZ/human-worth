#!/usr/bin/env python3
"""Deploy registered application modules into kind-lab. Secrets stay outside Git."""
import base64
import hashlib
import json
import os
import re
from pathlib import Path
import secrets
import subprocess
import time

from infra import HERE, STATE, KUBECONFIG, apply, kube, pin_images, run
from services import load_services, validate_manifest

REPO = HERE.parent.parent


def catalog_image_supported(image, expected_id=None):
    # Execute the actual local image by immutable ID, without secrets/network.
    # Candidate annotations/env and the mere presence of Voting prove nothing.
    probe='human-worth-catalog-probe-'+secrets.token_hex(8)
    try:
        image_id=run('docker','image','inspect',image,'--format','{{.Id}}',capture=True).stdout.strip()
        if not re.fullmatch(r'sha256:[a-f0-9]{64}',image_id) or expected_id is not None and image_id!=expected_id:
            return False
        result=subprocess.run(['docker','run','--name',probe,'--rm','--network=none','--read-only','--cap-drop=ALL',
                               '--pids-limit=16','--cpus=0.25','--memory=64m','--memory-swap=64m',
                               '--security-opt=no-new-privileges','--entrypoint','/service',image_id,
                               'catalog-protocol-version'],text=True,capture_output=True,timeout=15)
        if result.returncode or result.stdout.strip()!='voting-catalog-v1':return False
        if run('docker','image','inspect',image,'--format','{{.Id}}',capture=True).stdout.strip()!=image_id:return False
        evidence=STATE/'catalog-capabilities.json'
        known=json.loads(evidence.read_text()) if evidence.exists() else {}
        known[image]={'imageId':image_id,'protocol':'voting-catalog-v1'}
        temp=evidence.with_suffix('.tmp');temp.write_text(json.dumps(known,sort_keys=True)+'\n');temp.replace(evidence)
        return True
    except (subprocess.SubprocessError,OSError,ValueError):
        return False
    finally:
        # Killing a timed-out Docker CLI does not kill its container.
        subprocess.run(['docker','rm','-f',probe],text=True,capture_output=True,timeout=15,check=False)


def catalog_enabled(state=None):
    return ((state or STATE)/'voting-catalog-v1.enabled').exists()


def enable_catalog(state=None):
    # Conservatively persist BEFORE activation/draining, including failed first
    # attempts. Never remove this marker during rollback or process restart.
    state=state or STATE
    with (state/'voting-catalog-v1.enabled').open('w') as marker:
        marker.write('voting-catalog-v1\n');marker.flush();os.fsync(marker.fileno())
    fd=os.open(state,os.O_RDONLY)
    try:os.fsync(fd)
    finally:os.close(fd)


def catalog_capable(deployment):
    containers=deployment.get('spec',{}).get('template',{}).get('spec',{}).get('containers',[])
    return len(containers)==1 and any(item.get('name')=='VOTING_TARGET' and item.get('value')
        for item in containers[0].get('env',[])) and catalog_image_supported(containers[0].get('image',''))


def prepare_catalog_activation(workloads):
    if 'voting' not in workloads:
        if catalog_enabled():raise RuntimeError('Catalog-v1 enabled; refusing activation without Voting')
        return
    content=next(obj for obj in workloads['content'] if obj['kind']=='Deployment')
    if not catalog_capable(content):
        raise RuntimeError('Content image lacks verified catalog-v1 capability')
    first=not catalog_enabled()
    enable_catalog()
    current=json.loads(kube('-n','human-worth','get','deployment','content','--ignore-not-found','-o','json',capture=True).stdout or '{}')
    pods=json.loads(kube('-n','human-worth','get','pods','-l','app=content','-o','json',capture=True).stdout)['items']
    if first or (current and not catalog_capable(current)) or any(
            len(pod['spec']['containers'])!=1 or not catalog_image_supported(pod['spec']['containers'][0]['image']) for pod in pods):
        if current:kube('-n','human-worth','scale','deployment/content','--replicas=0')
        # Wait through graceful termination/in-flight decisions. Never overlap
        # an unguarded writer with new Voting/Content workers on first upgrade.
        if current or pods:
            kube('-n','human-worth','wait','--for=delete','pod','-l','app=content','--timeout=120s')


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
        if path.is_file() and not path.name.endswith('_test.go') and (path.parts[-1] in ['go.mod','go.sum','Dockerfile','Dockerfile.executor'] or any(part in ['cmd','internal','gen'] for part in path.relative_to(REPO/'backend').parts)):
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
        target=['--target','worker' if name=='challenge-worker' else 'service'] if ' AS service' in (REPO/'backend/Dockerfile').read_text() else []
        run('docker','build',*target,'--provenance=false','--sbom=false','--network=host','--build-arg','HTTP_PROXY','--build-arg','HTTPS_PROXY','--build-arg','SERVICE='+name,'--build-arg','REVISION='+version,'-f',str(REPO/'backend/Dockerfile'),'-t',image,str(REPO))
        run('kind','load','docker-image','--name','lab',image)
        result['images'][name]={'tag':image,'id':run('docker','image','inspect',image,'--format','{{.Id}}',capture=True).stdout.strip()}
    if 'challenge-worker' in services:
        image='human-worth/challenge-executor:'+version
        run('docker','build','--provenance=false','--sbom=false','--network=host','--build-arg','HTTP_PROXY','--build-arg','HTTPS_PROXY','-f',str(REPO/'backend/Dockerfile.executor'),'-t',image,str(REPO))
        run('kind','load','docker-image','--name','lab',image)
        pinned=None
        for node in run('kind','get','nodes','--name','lab',capture=True).stdout.splitlines():
            rows=run('docker','exec',node,'ctr','-n','k8s.io','images','ls',capture=True).stdout.splitlines()
            matches=[r.split()[2] for r in rows if r.split() and r.split()[0]=='docker.io/'+image]
            if len(matches)!=1 or not re.fullmatch('sha256:[a-f0-9]{64}',matches[0]):raise RuntimeError('Executor manifest digest unavailable')
            current='docker.io/human-worth/challenge-executor@'+matches[0]
            if pinned and pinned!=current:raise RuntimeError('Executor image differs across nodes')
            pinned=current
            run('docker','exec',node,'ctr','-n','k8s.io','images','tag','--force','docker.io/'+image,pinned,capture=True)
        result['executorImage']=pinned
    if source_hash()!=digest: raise RuntimeError('Source changed while building; rerun after edits finish')
    (STATE/'build.json').write_text(json.dumps(result,indent=2)+'\n')
    return result


def object_storage_config(state=None):
    """Explicit lab-only S3 config. No credentials or filesystem fallback in Git."""
    state = state or STATE
    path = Path(os.environ.get('ASSET_S3_CONFIG_FILE', str(state/'asset-s3.json')))
    if path.resolve().is_relative_to(REPO.resolve()):
        raise RuntimeError('Asset S3 secrets must be outside the repository')
    try:
        if path.stat().st_mode & 0o077:
            raise ValueError('unsafe permissions')
        config = json.loads(path.read_text())
        required = {'endpoint','bucket','region','access_key','secret_key','force_path_style'}
        if set(config) != required or type(config['force_path_style']) is not bool or any(not isinstance(config[k], str) or not config[k].strip() for k in required-{'force_path_style'}):
            raise ValueError('incomplete configuration')
        # The shipped lab NetworkPolicy grants access only to this in-cluster
        # private S3 service. Other destinations require a reviewed policy.
        if config['endpoint'] != 'http://asset-store.human-worth.svc.cluster.local:8333' or not config['force_path_style']:
            raise ValueError('endpoint is outside the lab storage policy')
        if not re.fullmatch(r'[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]', config['bucket']):
            raise ValueError('invalid bucket')
        return config
    except (OSError, ValueError, TypeError, AttributeError):
        raise RuntimeError('Asset requires a private, complete asset-s3.json and a pre-provisioned S3 bucket') from None


def database_secrets(services, storage=None, challenge_model=None):
    for name, spec in services.items():
        if not spec['database']: continue
        for role in (['owner','runtime','operator'] if name == 'identity' else ['owner','runtime']):
            password=private_file(STATE/(name+'-'+role+'.password'),secrets.token_urlsafe(32))
            username=name.replace('-', '_')+'_'+role
            secret(name+'-'+role+'-password',{'username':username,'password':password},'kubernetes.io/basic-auth')
            values={'database-url':'postgres://'+username+':'+password+'@database-rw.human-worth.svc.cluster.local:5432/human_worth?sslmode=verify-full&sslrootcert=/database-ca/ca.crt'}
            if name == 'asset' and role == 'runtime':
                if storage is None: raise RuntimeError('Asset S3 configuration missing')
                values.update({'s3-'+key.replace('_','-'): (str(value).lower() if isinstance(value,bool) else value) for key,value in storage.items()})
            if name == 'challenge' and role == 'runtime':
                if challenge_model is None:raise RuntimeError('Challenge model configuration missing')
                values['models']=challenge_model['model']
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
    if catalog_enabled() and 'voting' not in services:
        raise RuntimeError('Catalog-v1 enabled; refusing activation without Voting')
    challenge_model=None
    if 'challenge-worker' in services:
        from challenge_runtime import model_configuration, preflight, install as install_challenge_runtime
        challenge_model=model_configuration(STATE,REPO)
        preflight(kube)
    # Asset's private lab store is an explicit module dependency. It creates no
    # public endpoint and has no filesystem fallback; installation is idempotent.
    storage=None
    install_asset_storage=None
    if 'asset' in services:
        from object_store import configuration, install
        storage=configuration(STATE)
        install_asset_storage=lambda: install(storage)
    images=build_images(revision,services)
    workloads={name:manifests(name,images['images'][name]['tag']) for name in services}
    if 'voting' in services:
        content=next(obj for obj in workloads['content'] if obj['kind']=='Deployment')
        if not catalog_capable(content) or not catalog_image_supported(images['images']['content']['tag'],images['images']['content']['id']):
            raise RuntimeError('Content image lacks verified catalog-v1 capability')
    if before_activation: before_activation()
    if install_asset_storage: install_asset_storage()
    database_secrets(services,storage,challenge_model)
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
    prepare_catalog_activation(workloads)
    if challenge_model is not None:
        install_challenge_runtime(STATE,REPO,challenge_model,images['executorImage'],secret,kube,workloads)
    # Install all peer policies before waiting for a newly added dependency.
    for objects in workloads.values():
        for obj in objects:
            if obj['kind'] != 'Deployment':
                kube('apply','--server-side','--field-manager=human-worth-release','--force-conflicts','-f','-',input=json.dumps(obj))
    for name, spec in services.items():
        if name=='gateway' and 'voting' in services:
            kube('-n','human-worth','exec','deployment/content','--','/service','bootstrap-voting')
        obj=next(obj for obj in workloads[name] if obj['kind']=='Deployment')
        # Environment and TLS credentials are read on process startup; secret changes require a rollout.
        fingerprint=hashlib.sha256((STATE/(name+'.crt')).read_bytes())
        if spec['database']: fingerprint.update((STATE/(name+'-runtime.password')).read_bytes())
        if name=='asset': fingerprint.update(json.dumps(storage,sort_keys=True).encode())
        if name in ['challenge','challenge-worker']:
            fingerprint.update(json.dumps(challenge_model,sort_keys=True).encode())
            fingerprint.update(images['executorImage'].encode())
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
