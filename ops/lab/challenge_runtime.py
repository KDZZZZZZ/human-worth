"""Private model configuration and least-privilege worker execution deployment."""
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
from urllib.parse import urlsplit


def model_configuration(state, repo):
    path=Path(os.environ.get('CHALLENGE_MODEL_CONFIG_FILE',str(state.parent/'challenge-model.json')))
    try:
        if path.resolve().is_relative_to(repo.resolve()) or path.stat().st_mode & 0o077:raise ValueError()
        cfg=json.loads(path.read_text())
        if set(cfg)!={'base_url','model','protocol','api_key'} or any(not isinstance(v,str) or not v for v in cfg.values()):raise ValueError()
        u=urlsplit(cfg['base_url'])
        if u.scheme!='https' or not u.hostname or u.username or u.password or u.query or u.fragment or u.port not in (None,443) or cfg['protocol']!='completion':raise ValueError()
        if not re.fullmatch(r'[a-zA-Z0-9.-]+',u.hostname) or ',' in cfg['model']:raise ValueError()
        return cfg
    except (OSError,ValueError,TypeError):
        raise RuntimeError('Challenge requires a private HTTPS Completion model configuration outside Git') from None


def preflight(kube):
    # Runtime installation is an explicit infrastructure operation. Never turn
    # an absent sandbox into ordinary runc or begin a partial application rollout.
    runtime=json.loads(kube('get','runtimeclass','gvisor','-o','json',capture=True).stdout)
    if runtime.get('handler')!='runsc':raise RuntimeError('gVisor RuntimeClass is required before deploying Challenge')


def install(state, repo, model, image, secret, kube, workloads):
    if not re.fullmatch(r'[^\s]+@sha256:[a-f0-9]{64}',image or ''):raise RuntimeError('Pinned executor image required')
    # Install only the reviewed fixed isolation policy and scoped Job RBAC.
    kube('apply','--server-side','-f','-',input=(Path(__file__).parent.parent/'challenge/execution-isolation.yaml').read_text())
    profile={'kubeconfig':'/challenge-worker-secrets/kubeconfig','context':'executor','namespace':'human-worth-execution',
             'image':image,'runtime_class':'gvisor','worker_namespace':'human-worth','relay_url':'https://challenge-worker.human-worth.svc.cluster.local:9443','ca_file':'/tls/ca.crt'}
    config={'apiVersion':'v1','kind':'Config','current-context':'executor',
            'clusters':[{'name':'cluster','cluster':{'server':'https://kubernetes.default.svc','certificate-authority':'/api/ca.crt'}}],
            'users':[{'name':'worker','user':{'tokenFile':'/api/token'}}],
            'contexts':[{'name':'executor','context':{'cluster':'cluster','user':'worker','namespace':'human-worth-execution'}}]}
    secret('challenge-worker-runtime',{'model.json':json.dumps(model),'executor.json':json.dumps(profile),'kubeconfig':json.dumps(config)})
    # Allow only actual API endpoint addresses, not unrestricted cluster/host access.
    endpoint=json.loads(kube('-n','default','get','endpoints','kubernetes','-o','json',capture=True).stdout)
    peers=[];ports=[]
    for subset in endpoint.get('subsets',[]):
        for address in subset.get('addresses',[]):
            ip=ipaddress.ip_address(address['ip']);peers.append({'ipBlock':{'cidr':str(ip)+('/32' if ip.version==4 else '/128')}})
        for port in subset.get('ports',[]):
            item={'protocol':'TCP','port':port['port']}
            if item not in ports:ports.append(item)
    if not peers or not ports:raise RuntimeError('Kubernetes API endpoints unavailable')
    policy=next(o for o in workloads['challenge-worker'] if o['kind']=='NetworkPolicy')
    policy['spec']['egress'][-1]={'to':peers,'ports':ports}
    # Model keys stay in the worker Secret; the proxy only permits CONNECT to
    # the configured hostname. It receives no provider credential configuration.
    host=urlsplit(model['base_url']).hostname
    text=(Path(__file__).parent/'egress.yaml').read_text().replace('google-egress','model-egress').replace('google_hosts','model_hosts').replace('oauth2.googleapis.com www.googleapis.com',host).replace('{app: identity}','{app: challenge-worker}')
    parent=state/'egress-parent.json'
    if parent.exists():
        peer=json.loads(parent.read_text())
        address=str(ipaddress.IPv4Address(peer['host']));port=int(peer['port'])
        if not 1<=port<=65535:raise RuntimeError('Invalid parent proxy port')
        text=text.replace('    cache deny all','    cache_peer '+address+' parent '+str(port)+' 0 no-query default\n    never_direct allow all\n    cache deny all')
        text=text[:text.index('  egress:\n')]+'  egress:\n    - to: [{ipBlock: {cidr: '+address+'/32}}]\n      ports: [{protocol: TCP, port: '+str(port)+'}]\n'
    digest=hashlib.sha256(text.encode()).hexdigest()
    text=text.replace('metadata: {labels: {app: model-egress}}','metadata: {labels: {app: model-egress}, annotations: {human-worth.io/config: "'+digest+'"}}')
    kube('apply','--server-side','-f','-',input=text)
    kube('-n','human-worth','rollout','status','deployment/model-egress','--timeout=180s')
