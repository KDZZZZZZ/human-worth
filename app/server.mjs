import { createServer } from 'node:http';
import { readFileSync, realpathSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const root = new URL('../', import.meta.url);
const assets = new Map([
  ['/', ['public/index.html', 'text/html; charset=utf-8']],
  ['/index.html', ['public/index.html', 'text/html; charset=utf-8']],
  ['/style.css', ['public/style.css', 'text/css; charset=utf-8']],
]);

export function createApp(revision = 'development') {
  return createServer((request, response) => {
    response.setHeader('X-Content-Type-Options', 'nosniff');
    response.setHeader('Referrer-Policy', 'no-referrer');
    response.setHeader('Cache-Control', 'no-store');
    response.setHeader('Content-Security-Policy', "default-src 'none'; style-src 'self'; frame-ancestors 'none'");
    if (!['GET', 'HEAD'].includes(request.method)) {
      response.writeHead(405, { Allow: 'GET, HEAD' }).end();
      return;
    }
    const path = request.url.split('?')[0];
    if (path === '/api/health') {
      response.writeHead(200, { 'Content-Type': 'application/json; charset=utf-8' });
      response.end(request.method === 'HEAD' ? undefined : JSON.stringify({
        status: 'ok', service: 'human-worth', stage: 'foundation', revision,
      }));
      return;
    }
    const asset = assets.get(path);
    if (!asset) {
      response.writeHead(404).end();
      return;
    }
    response.writeHead(200, { 'Content-Type': asset[1] });
    response.end(request.method === 'HEAD' ? undefined : readFileSync(new URL(asset[0], root)));
  });
}

if (process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  let revision = 'development';
  try { revision = readFileSync(new URL('REVISION', root), 'utf8').trim(); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  const app = createApp(revision);
  app.listen(Number(process.env.PORT || 18090), process.env.HOST || '127.0.0.1', () => {
    console.log(JSON.stringify({ event: 'listening', port: app.address().port, revision }));
  });
  process.on('SIGTERM', () => app.close(() => process.exit(0)));
}
