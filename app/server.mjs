import { createServer, request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';
import { readFileSync, realpathSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const root = new URL('../', import.meta.url);
const assets = new Map([
  ['/', ['public/index.html', 'text/html; charset=utf-8']],
  ['/index.html', ['public/index.html', 'text/html; charset=utf-8']],
  ['/style.css', ['public/style.css', 'text/css; charset=utf-8']],
]);

function forwardedHeaders(headers) {
  const result = { ...headers };
  for (const name of ['connection', 'keep-alive', 'proxy-connection', 'proxy-authenticate',
    'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade',
    ...(headers.connection || '').split(',')]) delete result[name.trim().toLowerCase()];
  return result;
}

function proxyApi(request, response, upstream) {
  const send = upstream.protocol === 'https:' ? httpsRequest : httpRequest;
  const pending = send(upstream, {
    path: request.url, method: request.method,
    headers: { ...forwardedHeaders(request.headers), host: upstream.host },
    timeout: 30000,
  }, incoming => {
    response.writeHead(incoming.statusCode, forwardedHeaders(incoming.headers));
    incoming.on('error', () => response.destroy());
    incoming.pipe(response);
  });
  pending.on('timeout', () => pending.destroy(new Error('API upstream timeout')));
  pending.on('error', () => {
    if (response.destroyed) return;
    if (response.headersSent) response.destroy();
    else response.writeHead(502, { 'Content-Type': 'application/json' }).end(JSON.stringify({ error: 'API upstream unavailable' }));
  });
  request.on('error', () => pending.destroy());
  response.on('close', () => pending.destroy());
  request.pipe(pending);
}

export function createApp(revision = 'development', apiOrigin) {
  const upstream = apiOrigin ? new URL(apiOrigin) : undefined;
  if (upstream && (!['http:', 'https:'].includes(upstream.protocol) || upstream.username || upstream.password
    || upstream.pathname !== '/' || upstream.search || upstream.hash)) throw new Error('API origin must be an HTTP(S) origin without credentials or a path');
  return createServer((request, response) => {
    response.setHeader('X-Content-Type-Options', 'nosniff');
    response.setHeader('Referrer-Policy', 'no-referrer');
    response.setHeader('Cache-Control', 'no-store');
    response.setHeader('Content-Security-Policy', "default-src 'none'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'");
    const path = request.url.split('?')[0];
    if (upstream && path.startsWith('/api/')) {
      proxyApi(request, response, upstream);
      return;
    }
    if (!['GET', 'HEAD'].includes(request.method)) {
      response.writeHead(405, { Allow: 'GET, HEAD' }).end();
      return;
    }
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
  const frontendDev = process.argv.includes('--frontend-dev');
  if (frontendDev && process.env.NODE_ENV === 'production') throw new Error('Frontend development proxy cannot run in production');
  const apiOrigin = frontendDev ? process.env.FRONTEND_API_ORIGIN || 'http://123.56.161.234:18090' : undefined;
  const app = createApp(revision, apiOrigin);
  app.listen(Number(process.env.PORT || (frontendDev ? 18100 : 18090)), process.env.HOST || '127.0.0.1', () => {
    console.log(JSON.stringify({ event: 'listening', port: app.address().port, revision }));
  });
  process.on('SIGTERM', () => app.close(() => process.exit(0)));
}
