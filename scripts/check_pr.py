"""Validate branch direction using GitHub's event data, without shell interpolation."""
import json
import os
from pathlib import Path

if os.environ.get('GITHUB_EVENT_NAME') == 'pull_request':
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    pr = event['pull_request']
    base, head = pr['base']['ref'], pr['head']['ref']
    if base == 'main':
        assert head == 'dev' and pr['head']['repo']['full_name'] == event['repository']['full_name'], 'main only accepts this repository dev branch'
    elif base == 'dev':
        assert head not in {'dev', 'main'}, 'development PRs must use a task branch'
    else:
        raise AssertionError(f'Unexpected PR target: {base}')
print('PR direction: PASS (or not a pull_request event)')
