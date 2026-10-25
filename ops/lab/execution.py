#!/usr/bin/env python3
"""Install the pinned gVisor bundle only inside explicitly selected kind nodes."""
import argparse
import hashlib
import json
from pathlib import Path
import tarfile
import tempfile

from infra import KUBECONFIG, STATE, run

VERSION='20261019.0'
SHA512='7cbc2aeeee809dcccfb40e612504a1b76f2c119790cee59e482f7b35428cccc4f71705e7e322442d07d1f7d29b5d9e72e31319fc3088d6dcbec9fc7c1f0d8881'


def main(cluster='lab', kubeconfig=None, archive=None):
    if cluster not in ('lab','human-worth-backend-check'):raise ValueError('Only the project kind clusters are accepted')
    config=Path(kubeconfig) if kubeconfig else KUBECONFIG
    if not config.is_file():raise RuntimeError('Dedicated kubeconfig required')
    def kube(*args,**kwargs):return run('kubectl','--kubeconfig',str(config),'--context','kind-'+cluster,*args,**kwargs)
    nodes=run('kind','get','nodes','--name',cluster,capture=True).stdout.splitlines()
    actual=json.loads(kube('get','nodes','-o','json',capture=True).stdout)['items']
    if not nodes or set(nodes)!={n['metadata']['name'] for n in actual} or any(n['status']['nodeInfo']['architecture']!='amd64' for n in actual):
        raise RuntimeError('Expected matching amd64 kind nodes')
    bundle=Path(archive) if archive else STATE/('gvisor-'+VERSION+'.tar.bz2')
    if not bundle.exists():
        bundle.parent.mkdir(parents=True,exist_ok=True)
        temporary=bundle.with_suffix('.download')
        run('curl','--fail','--location','--retry','2','--max-time','240','--output',str(temporary),'https://storage.googleapis.com/gvisor/releases/release/'+VERSION+'/x86_64/gvisor.tar.bz2')
        if hashlib.sha512(temporary.read_bytes()).hexdigest()!=SHA512:raise RuntimeError('gVisor archive checksum mismatch')
        temporary.replace(bundle)
    if hashlib.sha512(bundle.read_bytes()).hexdigest()!=SHA512:raise RuntimeError('gVisor archive checksum mismatch')
    stanza='\n[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc]\n  runtime_type = "io.containerd.runsc.v1"\n'
    with tempfile.TemporaryDirectory(prefix='human-worth-gvisor-') as directory:
        with tarfile.open(bundle) as tar:tar.extractall(directory,filter='data')
        root=Path(directory)
        for path in root.rglob('*'):
            path.chmod(0o755 if path.is_dir() or path.stat().st_mode & 0o111 else 0o644)
        root.chmod(0o755)
        for node in nodes:
            original=run('docker','exec',node,'cat','/etc/containerd/config.toml',capture=True).stdout
            if 'io.containerd.grpc.v1.cri' not in original:raise RuntimeError('Unrecognized kind containerd configuration')
            if 'runtimes.runsc]' in original and stanza.strip() not in original:raise RuntimeError('Existing runsc configuration requires review')
            run('docker','cp',str(root)+'/.',node+':/usr/local/bin/')
            if stanza.strip() not in original:
                run('docker','exec','-i',node,'tee','/etc/containerd/config.toml',input=original+stanza,capture=True)
                run('docker','exec',node,'systemctl','restart','containerd')
            run('docker','exec',node,'/usr/local/bin/runsc','--version')
    kube('apply','--server-side','-f','-',input=json.dumps({'apiVersion':'node.k8s.io/v1','kind':'RuntimeClass','metadata':{'name':'gvisor'},'handler':'runsc'}))
    kube('wait','--for=condition=Ready','nodes','--all','--timeout=120s')
    print('Pinned gVisor installed in '+cluster+'; host Docker runtime unchanged',flush=True)


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--cluster',default='lab',choices=['lab','human-worth-backend-check'])
    parser.add_argument('--kubeconfig')
    parser.add_argument('--archive')
    args=parser.parse_args()
    main(args.cluster,args.kubeconfig,args.archive)
