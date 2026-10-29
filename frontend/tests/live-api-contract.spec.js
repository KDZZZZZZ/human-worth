import { test, expect } from '@playwright/test';

const account = { id: 'account-contract', displayName: 'API Reader' };
const session = { account, role: 'user', csrfToken: 'csrf-from-server' };
const reply = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

test('Sign in navigates to the actual auth endpoint and never sets a local session', async ({ page }) => {
  const seen = [];
  await page.addInitScript(() => sessionStorage.setItem('human-worth-preview-data', 'on'));
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    seen.push(path);
    if (path === '/api/me') return reply(route, { detail: 'Unauthorized' }, 401);
    if (path === '/api/auth/google') return route.fulfill({ contentType: 'text/html', body: '<h1>Authentication endpoint reached</h1>' });
    return reply(route, { items: [], nextCursor: null });
  });
  await page.goto('/');
  await page.getByRole('link', { name: 'Sign in', exact: true }).click();
  await expect(page).toHaveURL(/\/api\/auth\/google$/);
  await expect(page.getByRole('heading')).toHaveText('Authentication endpoint reached');
  expect(seen.filter(path => path === '/api/auth/google')).toHaveLength(1);
});

test('502 is visible and retry can recover without a fabricated authenticated state', async ({ page }) => {
  let failed = true;
  await page.route('**/api/**', route => {
    if (new URL(route.request().url()).pathname === '/api/me') {
      return failed ? route.fulfill({ status: 502, contentType: 'text/html', body: '<h1>Bad gateway</h1>' }) : reply(route, session);
    }
    return reply(route, { items: [], nextCursor: null });
  });
  await page.goto('/submit');
  await expect(page.getByRole('alert')).toContainText('Service temporarily unavailable (502)');
  await expect(page.getByRole('button', { name: 'Sign out', exact: true })).toHaveCount(0);
  failed = false;
  await page.getByRole('alert').getByRole('button', { name: 'Retry' }).click();
  await expect(page.getByRole('button', { name: 'Sign out', exact: true })).toBeVisible();
});

test('saved draft is restored from the API after a full reload', async ({ page }) => {
  let record, creates = 0, reads = 0;
  await page.route('**/api/**', route => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (path === '/api/me') return reply(route, session);
    if (path === '/api/tasks' && request.method() === 'POST') {
      creates++;
      expect(request.headers()['x-csrf-token']).toBe(session.csrfToken);
      record = { id: 'saved-api-draft', kind: 'task', revision: 1, state: 'draft', content: request.postDataJSON(), reviewId: null };
      return reply(route, record, 201);
    }
    if (path === '/api/me/tasks/saved-api-draft') { reads++; return reply(route, record); }
    return reply(route, { items: [], nextCursor: null });
  });
  await page.goto('/submit');
  await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
  await page.getByLabel('Task title', { exact: true }).fill('Stored by the API');
  await page.getByRole('button', { name: 'Save draft', exact: true }).click();
  await expect(page.locator('#review').getByRole('status')).toContainText('Draft saved.');
  await expect(page).toHaveURL(/\/submit\?edit=saved-api-draft$/);
  await page.reload();
  await expect(page.getByLabel('Task title', { exact: true })).toHaveValue('Stored by the API');
  expect(creates).toBe(1);
  expect(reads).toBeGreaterThan(0);
});

test('My submissions uses server filters and reopens the rejected work with its review reason', async ({ page }) => {
  const requests = [];
  const work = { side: 'human', title: 'Rejected draft', source: 'Author', artifacts: [{ kind: 'text', text: 'Complete original work' }], permissions: { cloudUse: false } };
  const submission = { id: 'entry-1', kind: 'entry', revision: 2, state: 'rejected', rejectionReason: 'Add the missing context', content: work };
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url());
    requests.push(url);
    if (url.pathname === '/api/me') return reply(route, session);
    if (url.pathname === '/api/me/entries/entry-1') return reply(route, submission);
    return reply(route, { items: url.searchParams.get('kind') === 'entry' && url.searchParams.get('state') === 'rejected' ? [submission] : [], nextCursor: null });
  });
  await page.goto('/me/submissions');
  await page.getByLabel('Submission type').selectOption('entry');
  await page.getByLabel('Status', { exact: true }).selectOption('rejected');
  await expect(page.getByRole('heading', { name: 'Rejected draft' })).toBeVisible();
  await expect(page.getByText('Reviewer note: Add the missing context')).toBeVisible();
  await page.getByRole('link', { name: 'Continue editing' }).click();
  await expect(page).toHaveURL(/\/submit\?entry=entry-1$/);
  await expect(page.getByLabel('The work', { exact: true })).toHaveValue('Complete original work');
  expect(requests.some(url => url.pathname === '/api/me/submissions' && url.searchParams.get('state') === 'rejected')).toBe(true);
});

test('expired session does not turn a failed save into success', async ({ page }) => {
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/me') return reply(route, session);
    return reply(route, { detail: 'Session expired' }, 401);
  });
  await page.goto('/submit');
  await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
  await page.getByLabel('Task title', { exact: true }).fill('Keep this unsaved text');
  await page.getByRole('button', { name: 'Save draft', exact: true }).click();
  await expect(page.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();
  await expect(page.getByLabel('Task title', { exact: true })).toHaveValue('Keep this unsaved text');
  await expect(page.getByText('Draft saved.', { exact: true })).toHaveCount(0);
});
