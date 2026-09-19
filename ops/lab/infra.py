#!/usr/bin/env python3
"""Create/reconcile only the isolated kind-lab infrastructure; no public cutover."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.request
from urllib.parse import urlsplit, urlunsplit

HERE = Path(__file__).resolve().parent
STATE = Path(os.environ.get('HUMAN_WORTH_LAB_DIR', str(Path.home() / '.config/human-worth/lab')))
KUBECONFIG = STATE.parent / 'lab.kubeconfig'
VERSIONS = json.loads((HERE / 'versions.json').read_text())


def run(*args, input=None, capture=False):
    return subprocess.run(args, input=input, text=True, check=True, capture_output=capture)


def kube(*args, input=None, capture=False):
    return run('kubectl', '--kubeconfig', str(KUBECONFIG), '--context', 'kind-lab', *args, input=input, capture=capture)


def apply(text):
    kube('apply', '--server-side', '-f', '-', input=text)


def pin_images(text):
    for tag, digest in VERSIONS['images'].items():
        text = text.replace('image: '+tag+'\n', 'image: '+tag+'@'+digest+'\n')
        text = text.replace('imageName: '+tag+'\n', 'imageName: '+tag+'@'+digest+'\n')
        text = text.replace('value: '+tag+'\n', 'value: '+tag+'@'+digest+'\n')
    return text


def manifest(name):
    spec = VERSIONS['manifests'][name]
    path = STATE / name
    if not path.exists():
        path.write_bytes(urllib.request.urlopen(spec['url'], timeout=60).read())
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != spec['sha256']:
        raise RuntimeError('Upstream manifest checksum mismatch: '+name)
    return pin_images(data.decode())


def host_proxy(nodes):
    """Translate a host loopback proxy into the kind bridge gateway, without changing the host proxy."""
    proxy = os.environ.get('HTTPS_PROXY') or os.environ.get('https_proxy')
    if not proxy: return None
    parsed = urlsplit(proxy)
    if parsed.hostname not in ['localhost','127.0.0.1','::1']: return None
    if parsed.username or parsed.password: raise RuntimeError('Authenticated loopback proxy needs a private node configuration')
    network = json.loads(run('docker','network','inspect','kind',capture=True).stdout)[0]
    gateway = next(v['Gateway'] for v in network['IPAM']['Config'] if ':' not in v['Gateway'])
    translated = urlunsplit((parsed.scheme,gateway+(':'+str(parsed.port) if parsed.port else ''),'','',''))
    dropin = '[Service]\n'+''.join('Environment="'+key+'='+translated+'"\n' for key in ['HTTP_PROXY','HTTPS_PROXY','http_proxy','https_proxy'])
    for name in nodes:
        existing = subprocess.run(['docker','exec',name,'cat','/etc/systemd/system/containerd.service.d/human-worth-proxy.conf'],text=True,capture_output=True)
        if existing.returncode == 0 and existing.stdout == dropin: continue
        run('docker','exec',name,'mkdir','-p','/etc/systemd/system/containerd.service.d')
        run('docker','exec','-i',name,'tee','/etc/systemd/system/containerd.service.d/human-worth-proxy.conf',input=dropin,capture=True)
        run('docker','exec',name,'systemctl','daemon-reload')
        run('docker','exec',name,'systemctl','restart','containerd')
    # This is an address, never an authentication credential.
    (STATE/'egress-parent.json').write_text(json.dumps({'host':gateway,'port':parsed.port or 80}))
    return translated


def main():
    STATE.mkdir(parents=True, mode=0o700, exist_ok=True)
    STATE.chmod(0o700)
    clusters = run('kind', 'get', 'clusters', capture=True).stdout.splitlines()
    if 'lab' not in clusters:
        run('kind', 'create', 'cluster', '--config', str(HERE/'kind.yaml'), '--kubeconfig', str(KUBECONFIG))
    if not KUBECONFIG.exists():
        raise RuntimeError('Existing lab requires its own kubeconfig; do not use another context')
    nodes = json.loads(kube('get', 'nodes', '-o', 'json', capture=True).stdout)['items']
    expected = {'lab-control-plane', 'lab-worker', 'lab-worker2', 'lab-worker3'}
    if {n['metadata']['name'] for n in nodes} != expected:
        raise RuntimeError('Unexpected lab nodes')
    host_proxy(sorted(expected))
    for node in nodes:
        name = node['metadata']['name']
        run('docker', 'update', '--cpus', '1', '--memory', '2g', '--memory-swap', '2g', name)
        capacity = node['status']['capacity']
        memory = capacity['memory']
        if not memory.endswith('Ki'):
            raise RuntimeError('Unexpected kubelet memory units')
        # kind sees the host's capacity. Reserve the excess so scheduling sees ~1 CPU/1.5GiB.
        reserved_cpu = int(capacity['cpu']) - 1
        reserved_mib = int(memory[:-2])//1024 - 1536
        if reserved_cpu < 0 or reserved_mib < 0:
            raise RuntimeError('Host capacity below the lab budget')
        original = run('docker', 'exec', name, 'cat', '/var/lib/kubelet/config.yaml', capture=True).stdout
        config = re.sub(r'\nsystemReserved:\n(?:[ \t]+[^\n]*\n)*', '\n', original).rstrip()
        config += '\nsystemReserved:\n  cpu: "'+str(reserved_cpu)+'"\n  memory: "'+str(reserved_mib)+'Mi"\n'
        if config != original:
            run('docker', 'exec', '-i', name, 'tee', '/var/lib/kubelet/config.yaml', input=config, capture=True)
            run('docker', 'exec', name, 'systemctl', 'restart', 'kubelet')
    calico = manifest('calico.yaml')
    calico = calico.replace('# - name: CALICO_IPV4POOL_CIDR\n            #   value: "192.168.0.0/16"', '- name: CALICO_IPV4POOL_CIDR\n              value: "10.244.0.0/16"')
    calico = calico.replace('cpu: 250m', 'cpu: 100m')
    apply(calico)
    kube('-n', 'kube-system', 'set', 'resources', 'daemonset/calico-node', '--containers=calico-node', '--requests=cpu=100m,memory=96Mi', '--limits=cpu=500m,memory=256Mi')
    kube('-n', 'kube-system', 'set', 'resources', 'deployment/calico-kube-controllers', '--requests=cpu=25m,memory=32Mi', '--limits=cpu=200m,memory=128Mi')
    kube('wait', '--for=condition=Ready', 'nodes', '--all', '--timeout=300s')
    dns_patch={'spec':{'template':{'spec':{'topologySpreadConstraints':[{'maxSkew':1,'topologyKey':'kubernetes.io/hostname','whenUnsatisfiable':'DoNotSchedule','labelSelector':{'matchLabels':{'k8s-app':'kube-dns'}}}]}}}}
    kube('-n','kube-system','patch','deployment','coredns','--type=merge','-p',json.dumps(dns_patch))
    apply(manifest('cnpg.yaml'))
    kube('-n', 'cnpg-system', 'rollout', 'status', 'deployment/cnpg-controller-manager', '--timeout=300s')
    apply((HERE/'namespace.yaml').read_text())
    print('lab infrastructure ready; public services unchanged', flush=True)


if __name__ == '__main__':
    main()
