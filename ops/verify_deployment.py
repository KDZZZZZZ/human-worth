#!/usr/bin/env python3
"""Wait for the CI-gated local controller to publish this exact dev revision."""
import argparse
import json
import os
from pathlib import Path
import re
import sys
import time
from urllib.request import Request, urlopen

from deploy import REPO, ci_passed
from deployment_health import healthy
sys.path.insert(0, str(Path(__file__).resolve().parent / 'lab'))
from services import load_services


def github_json(path):
    headers = {'Accept': 'application/vnd.github+json', 'User-Agent': 'human-worth-deployment-check'}
    if token := os.environ.get('GH_TOKEN'):
        headers['Authorization'] = 'Bearer ' + token
    # The GitHub token is never used for application probes.
    with urlopen(Request('https://api.github.com/repos/' + REPO + path, headers=headers), timeout=15) as response:
        return json.load(response)


def require_current_ci(sha):
    if github_json('/git/ref/heads/dev')['object']['sha'] != sha:
        raise RuntimeError('Deployment superseded by a newer dev revision')
    runs = github_json('/actions/workflows/ci.yml/runs?branch=dev&event=push&head_sha=' + sha + '&per_page=100')
    if not ci_passed(runs.get('workflow_runs', []), sha):
        raise RuntimeError('Latest matching dev push CI is not successful')


def wait_for_deployment(sha, checks, timeout=1800, services=()):
    if not re.fullmatch('[a-f0-9]{40}', sha):
        raise ValueError('Invalid deployment SHA')
    deadline = time.monotonic() + timeout
    while True:
        require_current_ci(sha)
        if healthy(sha, checks, deployed_services=services):
            require_current_ci(sha)
            print('Public deployment verified: ' + sha, flush=True)
            return
        if time.monotonic() >= deadline:
            raise RuntimeError('Deployment timed out: inspect human-worth-identity-deploy.service; HTTP health and all module checks must pass')
        print('Waiting for automatic deployment: ' + sha, flush=True)
        time.sleep(min(15, max(0, deadline - time.monotonic())))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('revision')
    parser.add_argument('--timeout', type=int, default=1800)
    options = parser.parse_args()
    services = load_services(Path(__file__).resolve().parent.parent)
    wait_for_deployment(options.revision, [check for spec in services.values() for check in spec['checks']], options.timeout, services)
    if summary := os.environ.get('GITHUB_STEP_SUMMARY'):
        with open(summary, 'a') as output:
            output.write('Deployed `' + options.revision + '` to https://worth.oopsbox.cn/; public health and registered module checks passed.\n')
