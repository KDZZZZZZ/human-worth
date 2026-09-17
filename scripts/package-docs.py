"""Package docs for ECS SendFile; large assets are fetched and verified on install."""
import gzip
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile

root = Path(__file__).resolve().parents[1]
output = root / 'dist'
vendor = json.loads((root / 'package-lock.json').read_text())['packages']['node_modules/swagger-ui-dist']
files = {name: (output / 'docs' / name).read_bytes() for name in
         ['index.html', 'swagger-init.js', 'docs.css', 'openapi.yaml', 'manifest.json']}
files['nginx.conf'] = (root / 'ops/nginx/human-worth.conf').read_bytes()
files['vendor.json'] = json.dumps({
    'url': vendor['resolved'],
    'integrity': vendor['integrity'],
    'previousConfigSha256': hashlib.sha256(Path(sys.argv[1]).read_bytes()).hexdigest(),
}, sort_keys=True).encode()
buffer = io.BytesIO()
with tarfile.open(fileobj=buffer, mode='w') as archive:
    for name, content in files.items():
        member = tarfile.TarInfo(name)
        member.size, member.mode, member.mtime = len(content), 0o644, 0
        archive.addfile(member, io.BytesIO(content))
content = gzip.compress(buffer.getvalue(), mtime=0)
sha = hashlib.sha256(content).hexdigest()
path = output / f'human-worth-docs-{sha}.tgz'
path.write_bytes(content)
print(json.dumps({'path': str(path), 'sha256': sha, 'bytes': len(content)}))
