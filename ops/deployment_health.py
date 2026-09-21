"""Read-only deployment probes, shared by the local reconciler and Actions."""
import json
import subprocess

ORIGIN = 'https://worth.oopsbox.cn'


def response(path, curl_options=()):
    result = subprocess.run(['curl', '--silent', '--show-error', '--noproxy', '*',
                             '--connect-timeout', '5', '--max-time', '5',
                             '--write-out', '\n%{http_code}', *curl_options, ORIGIN + path],
                            check=True, capture_output=True, text=True, timeout=10)
    body, status = result.stdout.rsplit('\n', 1)
    return int(status), json.loads(body)


def healthy(revision, checks=(), curl_options=(), deployed_services=()):
    try:
        for check in [{'path': '/api/health', 'status': 200}, *checks, {'path': '/api/health', 'status': 200}]:
            status, data = response(check['path'], curl_options)
            if status != check['status'] or not isinstance(data, dict):
                return False
            if check['path'] == '/api/health':
                if data.get('revision') != revision or data.get('status') != 'ok' or data.get('service') != 'human-worth':
                    return False
                if deployed_services:
                    modules=data.get('deployedServices')
                    if data.get('deploymentRevision') != revision or not isinstance(modules,list) or any(not isinstance(name,str) for name in modules) or sorted(modules) != sorted(deployed_services):
                        return False
            elif 'code' in check and data.get('code') != check['code']:
                return False
        return True
    except (subprocess.SubprocessError, OSError, ValueError):
        return False
