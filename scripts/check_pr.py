"""Validate branch names and PR direction using GitHub's event data."""
import json
import os
from pathlib import Path
import re

TASK_BRANCH = re.compile(
    r'(frontend|backend)/(feat|fix|refactor|perf|style|test|docs|build|ci|chore|revert)-'
    r'[a-z0-9]+(?:-[a-z0-9]+)*'
)

if os.environ.get('GITHUB_EVENT_NAME') == 'pull_request':
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    pr = event['pull_request']
    base, head = pr['base']['ref'], pr['head']['ref']
    if base == 'main':
        assert head == 'dev' and pr['head']['repo']['full_name'] == event['repository']['full_name'], 'main only accepts this repository dev branch'
    elif base == 'dev':
        assert TASK_BRANCH.fullmatch(head), (
            'Task branches must use frontend/<type>-<topic> or backend/<type>-<topic>; '
            'see docs/development.md#branch-naming'
        )
    else:
        raise AssertionError(f'Unexpected PR target: {base}')
print('PR direction and branch naming: PASS (or not a pull_request event)')
