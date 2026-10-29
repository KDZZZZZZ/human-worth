import { test } from 'node:test';
import assert from 'node:assert/strict';
import { designRecords, importDesign } from '../scripts/data/import-design.js';

function harness({ loseCreate = false, role = 'admin' } = {}) {
  const records = new Map(), creates = new Map(), comments = new Map(), journal = new Map(), calls = [];
  const storage = { getItem: key => journal.get(key) || null, setItem: (key, value) => journal.set(key, value) };
  const api = async (path, options = {}) => {
    calls.push({ path, ...options });
    if (path === '/me') return { account: { id: 'owner' }, role };
    if (path === '/tasks') {
      const key = options.headers['Idempotency-Key'];
      if (!creates.has(key)) {
        const record = { id: 'task-' + records.size, revision: 1, state: 'draft', content: structuredClone(options.body), reviewId: null };
        records.set(record.id, record); creates.set(key, record.id);
      }
      if (loseCreate) { loseCreate = false; throw Error('Response lost after commit'); }
      return structuredClone(records.get(creates.get(key)));
    }
    const task = path.match(/^\/me\/tasks\/([^/]+)(\/review-requests)?$/);
    if (task) {
      const record = records.get(task[1]);
      if (task[2]) { record.state = 'pending_review'; record.reviewId = record.id; }
      return structuredClone(record);
    }
    const review = path.match(/^\/admin\/reviews\/([^/]+)(\/decision)?$/);
    if (review) {
      const record = records.get(review[1]);
      if (review[2]) record.state = 'published';
      return { id: record.id, submission: structuredClone(record), status: record.state === 'published' ? 'approved' : 'pending', deliveryState: record.state === 'published' ? 'applied' : 'undecided' };
    }
    const publicTask = path.match(/^\/tasks\/([^/]+)(\/comments)?$/);
    if (publicTask) {
      const record = records.get(publicTask[1]);
      assert.equal(record.state, 'published');
      if (!publicTask[2]) return structuredClone(record.content);
      const key = options.headers['Idempotency-Key'];
      if (!comments.has(key)) comments.set(key, { id: 'comment-' + comments.size, body: options.body.body });
      return comments.get(key);
    }
    throw Error('Unexpected operation: ' + path);
  };
  return { api, storage, records, comments, calls };
}

test('imports exact design inputs, only the complete task is reviewed; repeat is read-only', async () => {
  const h = harness();
  const options = { api: h.api, storage: h.storage, accountId: 'owner' };
  const result = await importDesign(options);
  assert.equal(result.length, 9);
  assert.equal(result.filter(record => record.state === 'published').length, 1);
  assert.equal(result.filter(record => record.state === 'draft').length, 8);
  assert.equal(h.comments.size, 6);
  assert.deepEqual([...h.records.values()].map(record => record.content), designRecords().map(record => record.content));
  const previous = h.calls.length;
  await importDesign(options);
  assert.equal(h.records.size, 9);
  assert.ok(h.calls.slice(previous).every(call => !call.method));
  assert.ok(h.calls.every(call => !/vote|statistics|mcp|auth/.test(call.path)));
});

test('lost create response resumes with the same idempotency key without a duplicate', async () => {
  const h = harness({ loseCreate: true });
  const options = { api: h.api, storage: h.storage, accountId: 'owner' };
  await assert.rejects(importDesign(options), /Response lost/);
  await importDesign(options);
  assert.equal(h.records.size, 9);
  const writes = h.calls.filter(call => call.path === '/tasks');
  assert.equal(writes[0].headers['Idempotency-Key'], writes[1].headers['Idempotency-Key']);
});

test('ordinary account cannot import or approve content', async () => {
  const h = harness({ role: 'user' });
  await assert.rejects(importDesign({ api: h.api, storage: h.storage, accountId: 'owner' }), /administrator/);
  assert.equal(h.records.size, 0);
});

test('resuming never approves a draft that was modified after import', async () => {
  const h = harness({ loseCreate: true });
  const options = { api: h.api, storage: h.storage, accountId: 'owner' };
  await assert.rejects(importDesign(options));
  h.records.get('task-0').content.entries[0].artifacts[0].text = 'Unrelated edited content';
  await assert.rejects(importDesign(options), /saved draft was edited/);
  assert.ok(h.calls.every(call => !call.path.endsWith('/decision')));
});
