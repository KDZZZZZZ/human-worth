#!/usr/bin/env python3
"""Exercise actual Calico/Squid allow and deny paths, only inside kind-lab."""
import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import time

from infra import HERE, apply, kube, run


def main():
    source=HERE/'network_probe.go'
    image='human-worth/network-probe:'+hashlib.sha256(source.read_bytes()).hexdigest()[:16]
    with tempfile.TemporaryDirectory(prefix='human-worth-network-') as directory:
        temp=Path(directory)
        run('env','CGO_ENABLED=0','go','build','-o',str(temp/'probe'),str(source))
        shutil.copyfile('/etc/ssl/certs/ca-certificates.crt',temp/'ca-certificates.crt')
        (temp/'Dockerfile').write_text('FROM scratch\nCOPY probe /probe\nCOPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt\nUSER 65532:65532\nENTRYPOINT ["/probe"]\n')
        run('docker','build','--provenance=false','--sbom=false','-t',image,str(temp))
    run('kind','load','docker-image','--name','lab',image)
    for caller in ['identity','content','gateway','untrusted']:
        name='network-probe-'+caller
        # Matching real policy labels is intentional. A failing readiness probe
        # prevents these test Pods from ever entering application Service endpoints.
        pod={'apiVersion':'v1','kind':'Pod','metadata':{'name':name,'namespace':'human-worth','labels':{'app':caller}},'spec':{
            'restartPolicy':'Never','automountServiceAccountToken':False,'nodeSelector':{'human-worth.io/worker':'true'},
            'securityContext':{'runAsNonRoot':True,'runAsUser':65532,'runAsGroup':65532,'seccompProfile':{'type':'RuntimeDefault'}},
            'containers':[{'name':'probe','image':image,'imagePullPolicy':'Never','args':[caller],
                'securityContext':{'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}},
                'resources':{'requests':{'cpu':'25m','memory':'16Mi'},'limits':{'cpu':'100m','memory':'32Mi'}},
                'readinessProbe':{'exec':{'command':['/probe','not-ready']},'periodSeconds':1}}]}}
        kube('delete','pod',name,'-n','human-worth','--ignore-not-found','--wait=true')
        apply(json.dumps(pod))
        try:
            deadline=time.monotonic()+90
            phase=''
            while time.monotonic()<deadline:
                phase=kube('-n','human-worth','get','pod',name,'-o','jsonpath={.status.phase}',capture=True).stdout
                if phase in ['Succeeded','Failed']:break
                time.sleep(1)
            kube('-n','human-worth','logs',name)
            if phase!='Succeeded':raise RuntimeError('Network checks failed: '+caller)
        finally:
            kube('-n','human-worth','delete','pod',name,'--wait=false')


if __name__=='__main__':
    main()
