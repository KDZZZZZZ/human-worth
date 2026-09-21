"""Read the application deployment contract; no code or credentials in the catalog."""
from graphlib import TopologicalSorter
import json
from pathlib import Path

NAMES = ('identity', 'content', 'asset', 'voting', 'moderation', 'challenge',
         'discovery', 'challenge-worker', 'gateway')


def load_services(repo):
    repo = Path(repo).resolve()
    services = json.loads((repo / 'ops/lab/services.json').read_text())
    if not isinstance(services, dict) or not {'identity', 'gateway'} <= services.keys() or services.keys() - set(NAMES):
        raise ValueError('Invalid deployment service names')
    for name, spec in services.items():
        if not isinstance(spec, dict) or spec.keys() - {'dependencies', 'database', 'grants', 'checks'}:
            raise ValueError('Invalid deployment configuration: ' + name)
        deps = spec.get('dependencies')
        if not isinstance(deps, list) or any(not isinstance(dep, str) or dep not in services for dep in deps):
            raise ValueError('Unknown deployment dependency: ' + name)
        if type(spec.get('database')) is not bool:
            raise ValueError('Explicit database flag required: ' + name)
        paths = ['backend/cmd/' + name + '/main.go', 'ops/lab/' + name + '.yaml']
        if spec['database']:
            if not isinstance(spec.get('grants'), str):
                raise ValueError('Database grants file required: ' + name)
            paths.append(spec['grants'])
        for value in paths:
            path = (repo / value).resolve()
            if not path.is_relative_to(repo) or not path.is_file():
                raise ValueError('Missing or unsafe deployment input: ' + name)
        checks = spec.get('checks')
        if not isinstance(checks, list):
            raise ValueError('Explicit deployment checks required: ' + name)
        for check in checks:
            if not isinstance(check, dict) or check.keys() - {'path', 'status', 'code'}:
                raise ValueError('Invalid deployment check: ' + name)
            path = check.get('path', '')
            if not isinstance(path, str) or not path.startswith('/api/') or any(c in path for c in '?\r\n#'):
                raise ValueError('Deployment checks must use a fixed API GET path')
            if type(check.get('status')) is not int or check['status'] not in (200, 401, 403, 404):
                raise ValueError('Invalid expected deployment status')
    graph = {name: spec['dependencies'] for name, spec in services.items()}
    # The public gateway is activated after every registered module, including workers.
    graph['gateway'] = list(services.keys() - {'gateway'})
    order = TopologicalSorter(graph).static_order()
    return {name: services[name] for name in order}


def validate_manifest(objects, name, image):
    """Limit approved application data to that module's namespaced resources."""
    kinds = {'ServiceAccount', 'Service', 'Deployment', 'PodDisruptionBudget', 'NetworkPolicy'}
    seen = set()
    for obj in objects:
        kind, meta = obj.get('kind'), obj.get('metadata', {})
        if kind not in kinds or kind in seen or meta.get('name') != name or meta.get('namespace') != 'human-worth' or meta.get('ownerReferences'):
            raise ValueError('Manifest exceeds module ownership: ' + name)
        seen.add(kind)
        spec = obj.get('spec', {})
        if kind == 'ServiceAccount' and obj.get('automountServiceAccountToken') is not False:
            raise ValueError('Module ServiceAccount must not mount API credentials')
        if kind == 'Service':
            if spec.get('selector') != {'app': name} or spec.get('type', 'ClusterIP') not in ('ClusterIP', 'NodePort') or spec.get('externalIPs'):
                raise ValueError('Invalid module Service')
            if spec.get('type') == 'NodePort' and (name != 'gateway' or [p.get('nodePort') for p in spec.get('ports', [])] != [30443]):
                raise ValueError('Only the existing gateway NodePort is allowed')
        if kind in ('Deployment', 'PodDisruptionBudget') and spec.get('selector') != {'matchLabels': {'app': name}}:
            raise ValueError('Module selector must identify only itself')
        if kind == 'NetworkPolicy' and spec.get('podSelector') != {'matchLabels': {'app': name}}:
            raise ValueError('Module policy must identify only itself')
        if kind != 'Deployment':
            continue
        template = spec.get('template', {})
        pod = template.get('spec', {})
        containers = pod.get('containers', [])
        if template.get('metadata', {}).get('labels') != {'app': name} or pod.get('automountServiceAccountToken') is not False or pod.get('serviceAccountName') != name:
            raise ValueError('Invalid module Pod identity')
        if len(containers) != 1 or containers[0].get('image') != image or containers[0].get('name') != name or pod.get('initContainers'):
            raise ValueError('Module must run its approved application image')
        allowed = {name + '-tls', name + '-runtime', 'database-ca'}
        if name == 'identity':
            allowed.add('google-oauth')
        for volume in pod.get('volumes', []):
            if set(volume) - {'name', 'secret', 'emptyDir', 'configMap'} or ('secret' in volume and volume['secret'].get('secretName') not in allowed):
                raise ValueError('Unapproved module volume')
            if 'configMap' in volume and (name != 'gateway' or volume['configMap'].get('name') != 'release-status'):
                raise ValueError('Unapproved module ConfigMap')
        container = containers[0]
        if spec.get('replicas') != 2 or not all(container.get(probe) for probe in ['startupProbe', 'livenessProbe', 'readinessProbe']):
            raise ValueError('Module requires two replicas and health probes')
        if container.get('envFrom'):
            raise ValueError('Use explicit module environment variables')
        for env in container.get('env', []):
            source = env.get('valueFrom', {})
            if set(source) - {'secretKeyRef', 'fieldRef'} or ('secretKeyRef' in source and source['secretKeyRef'].get('name') not in allowed):
                raise ValueError('Unapproved module environment source')
    if not {'Deployment', 'ServiceAccount', 'NetworkPolicy'} <= seen:
        raise ValueError('Module workload, identity and network policy are required')
