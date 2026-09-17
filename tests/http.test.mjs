import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createApp } from '../app/server.mjs';

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
