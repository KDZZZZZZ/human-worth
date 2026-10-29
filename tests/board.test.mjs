import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const read = name => readFileSync(new URL(`../prototypes/frontend/${name}`, import.meta.url), 'utf8');

test('detail returns only to sibling lists and never rewrites global Discovery navigation', () => {
  const source = read('task-detail.js').split('// A validated return URL')[1].split('// Local submission draft')[0].split('\n').slice(1).join('\n');
  const origin = 'https://example.test/prototypes/frontend/';
  for (const [target, expected] of [
    ['board.html?category=Software&shown=6', 'board.html?category=Software&shown=6'],
    ['discovery.html?q=library', 'discovery.html?q=library'],
    ['https://evil.test/board.html', 'discovery.html'],
    ['//evil.test/board.html', 'discovery.html'],
    ['/other/board.html', 'discovery.html'],
    ['submit.html', 'discovery.html'],
    ['javascript:alert(1)', 'discovery.html'],
    ['http://[', 'discovery.html'],
  ]) {
    const back = { href: 'discovery.html', textContent: '← Back to Discovery' };
    const navigation = { href: 'discovery.html' };
    const location = new URL(`${origin}task-detail.html?return=${encodeURIComponent(target)}`);
    runInNewContext(source, { URL, URLSearchParams, location, document: {
      querySelector: selector => { assert.equal(selector, '#back-link'); return back; },
      querySelectorAll: () => [back, navigation],
    } });
    assert.equal(back.href, origin + expected, target);
    assert.equal(navigation.href, 'discovery.html');
    assert.equal(back.textContent, expected.startsWith('board.html') ? '← Back to Board' : '← Back to Discovery');
  }
});

test('Board fixture totals, category filters and URL limits remain consistent', () => {
  const board = read('board.js').split('const boards=[];')[0];
  for (const query of ['', '?category=Software&shown=6', '?category=invalid&shown=-4', '?shown=999']) {
    const context = { URLSearchParams, location: { search: query }, window: {} };
    runInNewContext(read('prototype-data.js'), context);
    context.HWPrototype = context.window.HWPrototype;
    runInNewContext(board + `
      globalThis.result = {total:count(samples), categories:Object.values(values),
        filtered:filtered().map(t=>t.category), category:state.category, limit:state.limit};
    `, context);
    const result = JSON.parse(JSON.stringify(context.result));
    assert.deepEqual(result.total, [8, 2, 4, 2]);
    assert.deepEqual(result.categories.reduce((sum, row) => sum.map((n, i) => n + row[i]), [0,0,0,0]), result.total);
    assert.ok(result.limit >= 3 && result.limit <= 8);
    if (query.includes('Software')) assert.deepEqual(result.filtered, ['Software', 'Software']);
    else assert.equal(result.category, 'All categories');
  }
});
