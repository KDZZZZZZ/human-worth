import { test, expect } from '@playwright/test';

test.beforeEach(async ({ page }) => {
  await page.route('**/api/**', route => route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ detail: 'Not signed in' }) }));
});
test('preview navigation, pagination, responsive layout and no real mutations', async ({ page }) => {
  const writes = [], errors = [];
  page.on('request', r => { if (r.url().includes('/api/') && r.method() !== 'GET') writes.push(r.url()); });
  page.on('pageerror', e => errors.push(e.message));
  await page.goto('/admin-preview');
  await expect(page.getByText('LOCAL DEMO', { exact: true })).toBeVisible();
  for (const [path, name] of [['reviews', 'Reviews'], ['reports', 'Reports'], ['runs', 'Challenge runs'], ['audit', 'Audit log']]) {
    await page.getByRole('navigation', { name: 'Admin navigation' }).getByRole('link', { name, exact: true }).click();
    await expect(page).toHaveURL(new RegExp('/admin-preview/' + path));
    await page.reload();
    await expect(page.getByRole('heading', { name, exact: true, level: 1 })).toBeVisible();
  }
  await page.goto('/admin-preview/reviews');
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
  await page.getByRole('combobox', { name: 'Status', exact: true }).selectOption('approved');
  await expect(page.getByText('Page 1', { exact: true })).toBeVisible();
  for (const width of [1440, 1024, 390]) {
    await page.setViewportSize({ width, height: 1000 });
    for (const path of ['', '/reviews', '/reports', '/runs', '/runs/new', '/audit']) {
      await page.goto('/admin-preview' + path);
      await expect(page.locator('.ad-panel').first()).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    }
  }
  expect(writes).toEqual([]); expect(errors).toEqual([]);
});
test('review rejection validates reason and separates pending delivery from applied', async ({ page }) => {
  await page.goto('/admin-preview/reviews/review-demo-001');
  await page.getByRole('button', { name: 'Reject', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Enter a reason');
  await page.getByRole('button', { name: 'Go back' }).click();
  await page.getByRole('textbox', { name: /^Reason\b/ }).fill('Please clarify the source.');
  await page.getByRole('button', { name: 'Reject', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page.getByText(/Decision recorded. Waiting for delivery/)).toBeVisible();
  await page.getByRole('button', { name: 'Refresh delivery status' }).click();
  await expect(page.locator('.ad-sticky')).toContainText('rejected');
  await expect(page.locator('.ad-sticky')).toContainText('applied');
  await page.reload();
  await expect(page.locator('.ad-sticky')).toContainText('rejected');
});
test('report resolution requires reason and never invents target content', async ({ page }) => {
  await page.goto('/admin-preview/reports');
  await page.getByRole('button', { name: 'report-demo-1', exact: true }).click();
  await expect(page.getByText(/does not include target content/)).toBeVisible();
  await page.getByRole('button', { name: 'Take down', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Enter a reason');
  await page.getByRole('button', { name: 'Go back' }).click();
  await page.getByLabel('Resolution reason').fill('Verified violation.');
  await page.getByRole('button', { name: 'Take down', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page.getByText(/take-down is not yet confirmed/)).toBeVisible();
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await page.getByRole('combobox', { name: 'Status', exact: true }).selectOption('actioned');
  await expect(page.getByRole('button', { name: 'report-demo-1', exact: true })).toBeVisible();
});
test('cancel, restart and candidate registration keep lifecycle boundaries', async ({ page }) => {
  await page.goto('/admin-preview/runs/run-demo-1');
  await page.getByRole('button', { name: 'Cancel run' }).click();
  await page.getByLabel('Cancellation reason').fill('End local preview run.');
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page.getByText(/Cancellation requested/)).toBeVisible();
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(page.locator('.ad-badges').first()).toContainText('cancelled');
  await page.getByRole('link', { name: 'Review & restart' }).click();
  await page.getByRole('button', { name: 'Select task', exact: true }).first().click();
  for (let i = 0; i < 6; i++) await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.getByRole('button', { name: 'Restart challenge', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page).toHaveURL(/\/admin-preview\/runs\/run-demo-/);
  await expect(page.locator('.ad-key-values')).toContainText('run-demo-1');
  await expect(page.locator('.ad-badges').first()).toContainText('queued');
  await page.goto('/admin-preview/runs/run-demo-2');
  await page.getByRole('button', { name: 'Register candidate' }).click();
  await expect(page.getByText(/pending review/).last()).toBeVisible();
  await page.getByRole('link', { name: 'Open review', exact: true }).click();
  await expect(page.locator('.ad-sticky')).toContainText('pending');
});
test('seven-step creation validates limits and creates a queued demo record', async ({ page }) => {
  await page.goto('/admin-preview/runs/new');
  await page.getByRole('button', { name: 'Select task', exact: true }).first().click();
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.getByLabel('Execution description', { exact: true }).fill('Write the library notice.');
  await page.getByLabel('Work requirements', { exact: true }).fill('Plain text, less than 80 words.');
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  for (let i = 0; i < 3; i++) {
    await page.getByRole('textbox', { name: /^Model ID\b/ }).fill('demo-model');
    await page.getByRole('button', { name: 'Continue', exact: true }).click();
  }
  await page.getByLabel('Ranker fit threshold', { exact: true }).fill('1');
  await page.getByLabel('Minimum comparable pairs', { exact: true }).fill('5');
  await page.getByLabel('Ranker round limit', { exact: true }).fill('5');
  await page.getByLabel('Packer / executor round limit', { exact: true }).fill('8');
  await page.getByRole('spinbutton', { name: /^Budget amount\b/ }).fill('100');
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('strictly between');
  await page.getByLabel('Ranker fit threshold', { exact: true }).fill('0.8');
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await page.getByRole('button', { name: 'Launch challenge', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page.locator('.ad-badges').first()).toContainText('queued');
});
test('live admin revalidates cached role and denies ordinary users before any admin request', async ({ page }) => {
  const adminRequests = [];
  await page.addInitScript(() => sessionStorage.setItem('human-worth:session', JSON.stringify({ account: { id: 'u', displayName: 'Stale admin' }, role: 'admin', csrfToken: 'old' })));
  await page.route('**/api/me', r => r.fulfill({ json: { account: { id: 'u', displayName: 'Ordinary user' }, role: 'user', csrfToken: 'fresh' } }));
  page.on('request', r => { if (r.url().includes('/api/admin/')) adminRequests.push(r.url()); });
  await page.goto('/admin/reviews');
  await expect(page.getByRole('heading', { name: 'Administrator access required' })).toBeVisible();
  expect(adminRequests).toEqual([]);
});
test('live review writes only the decision with CSRF; errors do not fall back to demo', async ({ page }) => {
  await page.route('**/api/me', r => r.fulfill({ json: { account: { id: 'admin-live', displayName: 'Administrator' }, role: 'admin', csrfToken: 'csrf-test' } }));
  await page.route('**/api/admin/reviews/live-review', r => r.fulfill({ json: { id: 'live-review', status: 'pending', deliveryState: 'undecided', decision: null, createdAt: '2026-10-30T02:00:00Z', submission: { kind: 'task', id: 'task', authorId: 'owner', revision: 9, content: { title: 'Real contract fixture', summary: 'Brief', description: 'Content', entries: [], taskAttachments: [] } } } }));
  let body, csrf;
  await page.route('**/api/admin/reviews/live-review/decision', r => { body = r.request().postDataJSON(); csrf = r.request().headers()['x-csrf-token']; return r.fulfill({ status: 409, json: { detail: 'Revision conflict. Reload the current record.' } }); });
  await page.goto('/admin/reviews/live-review');
  await page.getByRole('button', { name: 'Approve', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Revision conflict');
  expect(body).toEqual({ decision: 'approve', reason: '' }); expect(csrf).toBe('csrf-test');
  await expect(page.getByText('LOCAL DEMO', { exact: true })).toHaveCount(0);
});


