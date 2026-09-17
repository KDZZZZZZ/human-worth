import { copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';

const root = new URL('../', import.meta.url);
const output = new URL('dist/docs/', root);
const files = new Map([
  ...['index.html', 'swagger-init.js', 'docs.css'].map(name => [name, `docs/swagger/${name}`]),
  ['openapi.yaml', 'openapi.yaml'],
  ...['swagger-ui-bundle.js', 'swagger-ui.css', 'favicon-32x32.png', 'LICENSE', 'NOTICE', 'swagger-ui-bundle.js.LICENSE.txt']
    .map(name => [name, `node_modules/swagger-ui-dist/${name}`]),
]);

rmSync(output, { recursive: true, force: true });
mkdirSync(output, { recursive: true });
const hashes = {};
for (const [name, source] of files) {
  copyFileSync(new URL(source, root), new URL(name, output));
  hashes[name] = createHash('sha256').update(readFileSync(new URL(name, output))).digest('hex');
}
const { version } = JSON.parse(readFileSync(new URL('node_modules/swagger-ui-dist/package.json', root)));
writeFileSync(new URL('manifest.json', output), JSON.stringify({ swaggerUiVersion: version, files: hashes }, null, 2) + '\n');
console.log(`Swagger UI ${version}: ${files.size} files written to ${fileURLToPath(output)}`);
