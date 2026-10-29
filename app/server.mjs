import { createServer, request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';
import { readFileSync, realpathSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const root = new URL('../', import.meta.url);
const assets = new Map([
  ['/', ['public/index.html', 'text/html; charset=utf-8']],
  ['/index.html', ['public/index.html', 'text/html; charset=utf-8']],
  ['/style.css', ['public/style.css', 'text/css; charset=utf-8']],
  ['/brand/logo.png', ['public/brand/logo.png', 'image/png']],
]);
// Explicit development-only allowlist: never expose repository files or test scripts.
const frontendAssets = new Map([
  ...['index.html', 'discovery.html', 'task-detail.html', 'submit.html', 'login.html', 'board.html'].map(name => [`/${name}`, [name, 'text/html; charset=utf-8']]),
  ...['task-detail.css', 'submit.css', 'login.css', 'board.css', 'prototype-nav.css', 'fonts/fonts.css'].map(name => [`/${name}`, [name, 'text/css; charset=utf-8']]),
  ...['prototype-data.js', 'task-detail.js', 'submit.js', 'login.js', 'board.js'].map(name => [`/${name}`, [name, 'text/javascript; charset=utf-8']]),
  ...['source-serif-4-display-400', 'source-serif-4-400', 'source-sans-3-400', 'source-sans-3-500', 'source-sans-3-600', 'ibm-plex-mono-400'].map(name => [`/fonts/${name}.ttf`, [`fonts/${name}.ttf`, 'font/ttf']]),
  ['/library-context.png', ['library-context.png', 'image/png']],
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
    response.setHeader('Content-Security-Policy', "default-src 'none'; img-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'");
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
    if (upstream) {
      if (path === '/' && new URL(request.url, 'http://localhost').searchParams.get('login') === 'cancelled') {
        response.writeHead(303, { Location: '/login.html?login=cancelled' }).end();
        return;
      }
      const frontend = frontendAssets.get(path === '/' ? '/index.html' : path);
      if (frontend) {
        // Existing prototype pages use inline styles/scripts. This policy is dev-only.
        response.setHeader('Content-Security-Policy', "default-src 'none'; img-src 'self' blob: data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'");
        response.writeHead(200, { 'Content-Type': frontend[1] });
        response.end(request.method === 'HEAD' ? undefined : readFileSync(new URL(`prototypes/frontend/${frontend[0]}`, root)));
        return;
      }
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
  const apiOrigin = frontendDev ? process.env.FRONTEND_API_ORIGIN || 'https://worth.oopsbox.cn' : undefined;
  const app = createApp(revision, apiOrigin);
  app.listen(Number(process.env.PORT || (frontendDev ? 18100 : 18090)), process.env.HOST || '127.0.0.1', () => {
    console.log(JSON.stringify({ event: 'listening', port: app.address().port, revision }));
  });
  process.on('SIGTERM', () => app.close(() => process.exit(0)));
}
