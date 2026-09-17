"""Check the approved scenario snapshot and repo-local Markdown links."""
import hashlib
import re
from pathlib import Path
from urllib.parse import unquote

root = Path(__file__).resolve().parents[1]
prd = (root / 'docs/prd.md').read_text()
snapshot = prd.split('<!-- trusted-scenarios:start -->\n', 1)[1].split('<!-- trusted-scenarios:end -->', 1)[0]
assert hashlib.sha256(snapshot.encode()).hexdigest() == '1b02576ba200b4fba0f5fc233650a53b1c241384426de41c7f62e81c00e38b04', 'Approved snapshot changed; record explicit human revision before updating this check'
for file in [root / 'AGENTS.md', root / 'README.md', *root.glob('docs/**/*.md')]:
    text = file.read_text()
    assert text.count('```') % 2 == 0, f'Unclosed code fence: {file}'
    for link in re.findall(r'\[[^\]]+\]\(([^)]+)\)', text):
        link = unquote(link.strip('<>')).split('#')[0]
        if not link or re.match(r'[a-z]+://', link) or link.startswith('/home/oops/'):
            continue
        assert (file.parent / link).exists(), f'Broken local link in {file}: {link}'
print('Trusted scenario snapshot and document links: PASS')
