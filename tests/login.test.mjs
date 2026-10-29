import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const source = readFileSync(new URL('../prototypes/frontend/login.js', import.meta.url), 'utf8');
const validSession = { account: { id: 'acct_test', displayName: '<img src=x onerror=alert(1)>' }, role: 'user', csrfToken: 'test-csrf' };
function page(responses, protocol = 'https:', search = '') {
  const elements = new Map();
  const events = {};
  const calls = [];
  const element = id => {
    if (!elements.has(id)) elements.set(id, { hidden: true, textContent: '', attributes: {},
      setAttribute(key, value) { this.attributes[key] = value; },
      getAttribute(key) { return this.attributes[key]; },
      removeAttribute(key) { delete this.attributes[key]; } });
    return elements.get(id);
  };
  runInNewContext(source, { document: { querySelector: element }, window: { addEventListener: (key, fn) => { events[key] = fn; } },
    location: { protocol, search }, URLSearchParams, AbortSignal,
    fetch: async (path, options) => {
      calls.push({ path, options });
      const next = responses.shift();
      if (next instanceof Error) throw next;
      return { status: next.status, ok: next.status >= 200 && next.status < 300, json: async () => next.body };
    } });
  return { element, calls, load: events.pageshow };
}

test('anonymous and cancelled states never claim authentication; HTTP preview cannot start OAuth', async () => {
  for (const protocol of ['http:', 'https:']) {
    const p = page([{ status: 401 }], protocol, '?login=cancelled');
    await p.load();
    assert.match(p.element('#login-status').textContent, /cancelled/);
    assert.equal(p.element('#signed-in').hidden, true);
    assert.equal(p.element('#google-login').hidden, protocol === 'http:');
    assert.equal(p.calls[0].path, '/api/me');
    assert.equal(p.calls[0].options.cache, 'no-store');
  }
});

test('unavailable, disabled, malformed and network responses offer retry without false sign-in', async () => {
  for (const response of [{ status: 404 }, { status: 403 }, { status: 503 }, { status: 200, body: {} }, new Error('offline')]) {
    const p = page([response, { status: 401 }]);
    await p.load();
    assert.equal(p.element('#signed-in').hidden, true);
    assert.equal(p.element('#retry').hidden, false);
    await p.element('#retry').onclick();
    assert.equal(p.element('#signed-out').hidden, false);
  }
});

test('session uses plain text; logout sends CSRF and accepts only 204 or expired 401', async () => {
  for (const code of [204, 401, 403, 500]) {
    const p = page([{ status: 200, body: validSession }, { status: code }]);
    await p.load();
    assert.equal(p.element('#account-name').textContent, validSession.account.displayName);
    assert.equal(p.element('#signed-in').hidden, false);
    await p.element('#logout').onclick();
    assert.equal(p.calls[1].path, '/api/auth/logout');
    assert.equal(p.calls[1].options.method, 'POST');
    assert.equal(p.calls[1].options.headers['X-CSRF-Token'], 'test-csrf');
    assert.equal(p.element('#signed-in').hidden, [204, 401].includes(code));
    if (code >= 403) assert.match(p.element('#login-status').textContent, /not confirmed/);
  }
});

test('back navigation revalidates identity and allows Google navigation again', async () => {
  const p = page([{ status: 401 }, { status: 401 }]);
  await p.load();
  p.element('#google-login').onclick({ preventDefault() { assert.fail('First navigation should be native'); } });
  let blocked = false;
  p.element('#google-login').onclick({ preventDefault() { blocked = true; } });
  assert.equal(blocked, true);
  await p.load();
  assert.equal(p.element('#google-login').getAttribute('aria-disabled'), undefined);
  assert.equal(p.calls.length, 2);
});
