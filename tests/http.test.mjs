import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../app/server.mjs';
import { mkdtemp, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';

let app, origin;
before(async () => {
  app = createApp('test-revision');
  await new Promise(resolve => app.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${app.address().port}`;
});
after(() => new Promise(resolve => app.close(resolve)));

test('public frontend and API health are served through real HTTP', async () => {
  const page = await fetch(origin);
  assert.equal(page.status, 200);
  assert.match(await page.text(), /Human Worth/);
  const css = await fetch(`${origin}/style.css`);
  assert.equal(css.status, 200);
  const health = await fetch(`${origin}/api/health`);
  assert.deepEqual(await health.json(), {
    status: 'ok', service: 'human-worth', stage: 'foundation', revision: 'test-revision',
  });
});

test('repository files, credentials, and nonexistent product APIs are not public routes', async () => {
  for (const path of ['/.env', '/.git/config', '/AGENTS.md', '/api/tasks', '/%2e%2e/AGENTS.md']) {
    assert.equal((await fetch(`${origin}${path}`)).status, 404, path);
  }
  const response = await fetch(`${origin}/api/health`, { method: 'POST', body: 'anything' });
  assert.equal(response.status, 405);
});

test('real Node entrypoint starts through the deployment current symlink', { timeout: 10000 }, async () => {
  const folder = await mkdtemp(join(tmpdir(), 'human-worth-entry-'));
  const current = join(folder, 'current');
  await symlink(fileURLToPath(new URL('../', import.meta.url)), current, 'dir');
  const child = spawn(process.execPath, [join(current, 'app/server.mjs')], {
    env: { ...process.env, NODE_ENV: 'production', FRONTEND_API_ORIGIN: 'http://127.0.0.1:1', HOST: '127.0.0.1', PORT: '0' }, stdio: ['ignore', 'pipe', 'pipe'],
  });
  try {
    const port = await new Promise((resolve, reject) => {
      let output = '';
      child.once('error', reject);
      child.once('exit', code => reject(new Error(`Entrypoint exited before listening: ${code}`)));
      child.stdout.on('data', data => {
        output += data;
        if (output.includes('\n')) resolve(JSON.parse(output.split('\n')[0]).port);
      });
    });
    const response = await fetch(`http://127.0.0.1:${port}/api/health`);
    assert.equal(response.status, 200);
    assert.equal((await response.json()).service, 'human-worth');
  } finally {
    child.kill('SIGTERM');
    await rm(folder, { recursive: true, force: true });
  }
});

test('frontend development forwards API requests to its upstream and keeps pages local', async () => {
  const received = [];
  const upstream = createServer(async (request, response) => {
    let body = '';
    for await (const chunk of request) body += chunk;
    received.push({ method: request.method, url: request.url, body, host: request.headers.host });
    response.writeHead(201, { 'Content-Type': 'application/json', 'X-Upstream': 'public-api' });
    response.end(JSON.stringify({ revision: 'public-revision' }));
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  const publicOrigin = `http://127.0.0.1:${upstream.address().port}`;
  const frontend = createApp('local-revision', publicOrigin);
  await new Promise(resolve => frontend.listen(0, '127.0.0.1', resolve));
  const local = `http://127.0.0.1:${frontend.address().port}`;
  try {
    const page = await fetch(local);
    assert.match(await page.text(), /Human Worth/);
    assert.match(page.headers.get('content-security-policy'), /connect-src 'self'/);
    assert.equal((await fetch(`${local}/apiary`)).status, 404);
    assert.equal(received.length, 0);
    const health = await fetch(`${local}/api/health`);
    assert.equal(health.status, 201);
    assert.equal((await health.json()).revision, 'public-revision');
    const result = await fetch(`${local}/api/tasks?cursor=a%2Fb`, { method: 'POST', body: 'task-body' });
    assert.equal(result.status, 201);
    assert.equal(result.headers.get('x-upstream'), 'public-api');
    await result.text();
    assert.deepEqual(received[1], { method: 'POST', url: '/api/tasks?cursor=a%2Fb', body: 'task-body', host: new URL(publicOrigin).host });
    await new Promise(resolve => upstream.close(resolve));
    const unavailable = await fetch(`${local}/api/health`);
    assert.equal(unavailable.status, 502);
    assert.deepEqual(await unavailable.json(), { error: 'API upstream unavailable' });
  } finally {
    await new Promise(resolve => frontend.close(resolve));
    if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
  }
});

test('production refuses the frontend development proxy', () => {
  const result = spawnSync(process.execPath, ['app/server.mjs', '--frontend-dev'], {
    cwd: fileURLToPath(new URL('../', import.meta.url)),
    env: { ...process.env, NODE_ENV: 'production', PORT: '0' }, encoding: 'utf8', timeout: 5000,
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Frontend development proxy cannot run in production/);
});
