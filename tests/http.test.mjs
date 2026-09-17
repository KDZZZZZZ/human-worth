import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../app/server.mjs';
import { mkdtemp, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';

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
    env: { ...process.env, HOST: '127.0.0.1', PORT: '0' }, stdio: ['ignore', 'pipe', 'pipe'],
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
