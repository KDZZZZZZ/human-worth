import { chromium } from '@playwright/test';
import fs from 'node:fs';
const out = 'artifacts/live-api';
fs.mkdirSync(out, { recursive: true });
const base = process.env.FRONTEND_URL || 'http://127.0.0.1:18100';
const checks = [];
let task;
for (const path of ['/api/health', '/api/tasks?limit=1', '/api/board']) {
  try {
    const response = await fetch(base + path, { signal: AbortSignal.timeout(10000) });
    checks.push({ path, status: response.status });
    if (response.ok && path.startsWith('/api/tasks')) task = (await response.json()).items[0];
  } catch (error) { checks.push({ path, error: error.message }); }
}
const browser = await chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || 'msedge' });
const errors = [];
try {
  for (const width of [1440, 390]) {
    for (const [name, path] of [['home', '/'], ['discovery', '/discovery'], ['submit', '/submit'], ...(task ? [['task', '/tasks/' + encodeURIComponent(task.id)]] : [])]) {
      const page = await browser.newPage({ viewport: { width, height: 1000 } });
      page.on('pageerror', error => errors.push({ name, width, message: error.message }));
      await page.goto(base + path);
      await page.locator('h1').first().waitFor();
      await page.evaluate(() => document.fonts.ready);
      await page.screenshot({ path: out + '/' + name + '-' + width + '.png', fullPage: true });
      checks.push({ name, width, overflow: await page.evaluate(() => document.documentElement.scrollWidth > innerWidth) });
      await page.close();
    }
  }
} finally { await browser.close(); }
fs.writeFileSync(out + '/checks.json', JSON.stringify({ checks, errors }, null, 2));
console.log(JSON.stringify({ checks, errors }, null, 2));
if (errors.length || checks.some(check => check.error || check.overflow || check.status >= 400)) process.exitCode = 1;
