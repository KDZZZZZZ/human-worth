import { test, expect } from '@playwright/test';
import { emptyWork, missingWorkFields } from '../src/state/models.js';

const complete = (side, title) => ({ side, title, source: 'Original source', artifacts: [{ kind: 'text', text: 'Complete work' }], permissions: { cloudUse: false }, ...(side === 'agent' ? { agentConfiguration: { model: 'Example model' } } : {}) });

for (const [name, agents, count, ready] of [
  ['one complete', [complete('agent', 'First agent')], 1, true],
  ['all complete', [complete('agent', 'First agent'), complete('agent', 'Second agent')], 2, true],
  ['partially complete', [complete('agent', 'First agent'), emptyWork('agent')], 1, false],
  ['none complete', [emptyWork('agent'), emptyWork('agent')], 0, false],
  ['empty', [], 0, false],
]) {
  test(`submission readiness: ${name}`, async ({ page }) => {
    const content = { title: 'Shared task', summary: 'Introduction', description: 'Instructions', taskAttachments: [], entries: [complete('human', 'Human work'), ...agents] };
    const record = { id: 'readiness', revision: 1, state: 'draft', content };
    const writes = [];
    await page.route('**/api/**', route => {
      const req = route.request(), path = new URL(req.url()).pathname;
      if (req.method() !== 'GET') writes.push(path);
      const body = path === '/api/me' ? { account: { id: 'reader', displayName: 'Reader' }, role: 'user', csrfToken: 'csrf' } : record;
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.goto('/submit?edit=readiness');
    await expect(page.locator('.review-sheet')).toContainText(`${agents.length} Agent added (${count} complete)`);
    await expect(page.locator('.checklist li').filter({ hasText: 'Agent works:' })).toHaveClass(ready ? 'complete' : '');
    const chapter = page.locator('.chapter.agent');
    await expect(chapter.locator('.chapter-readiness')).toContainText(ready ? `${count} complete in this draft` : agents.length ? `${count} of ${agents.length} complete` : 'Add at least one work');
    if (agents.some(work => !work.title)) {
      await expect(chapter.getByText(/Missing: Work title; Source; Agent model; Work text or uploaded file; Cloud-use permission/).first()).toBeVisible();
      await chapter.getByRole('button', { name: 'Untitled work' }).first().click();
      await expect(page.getByRole('radio', { name: 'Do not allow', exact: true })).not.toBeChecked();
      await page.getByRole('button', { name: 'Close work editor' }).click();
    }
    await page.getByRole('button', { name: 'Submit for review', exact: true }).click();
    if (ready) {
      await expect(page.locator('#review').getByRole('status')).toContainText('Submitted for review.');
      expect(writes).toEqual(['/api/me/tasks/readiness/review-requests']);
    } else {
      await expect(page.getByRole('alert')).toBeVisible();
      expect(writes).toEqual([]);
      if (agents.length) await expect(page.getByRole('alert')).toContainText('Untitled work');
    }
  });
}

test('work validation accepts either permission choice and requires a real file reference', () => {
  for (const allowed of [true, false]) expect(missingWorkFields({ ...complete('agent', 'Agent'), permissions: { cloudUse: allowed } })).toEqual([]);
  expect(missingWorkFields({ ...complete('human', 'Human'), artifacts: [{ kind: 'file' }] })).toContain('Work text or uploaded file');
  expect(missingWorkFields({ ...complete('human', 'Human'), artifacts: [{ kind: 'link', url: 'https://example.com' }] })).toContain('Work text or uploaded file');
});
