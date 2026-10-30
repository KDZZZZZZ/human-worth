import { chromium } from "@playwright/test";
import fs from "node:fs";

const output = "artifacts/board-login-live";
fs.mkdirSync(output, { recursive: true });
const endpoints = [];
for (const path of ["/api/health", "/api/board", "/api/tasks?order=hot&limit=3", "/api/me"]) {
  const response = await fetch((process.env.FRONTEND_TEST_ORIGIN || "http://127.0.0.1:18100") + path, { signal: AbortSignal.timeout(15000) });
  const body = await response.json();
  endpoints.push({ path, status: response.status, ...(path === "/api/board" ? { overall: body.overall, categories: body.categories?.length } : {}), ...(path.includes("order=hot") ? { tasks: body.items?.length } : {}) });
  if (!(path === "/api/me" ? response.status === 401 : response.ok)) throw Error("Unexpected response from " + path);
}
const browser = await chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "msedge" });
const errors = [], requests = [];
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, reducedMotion: "reduce" });
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => { if (new URL(request.url()).pathname.startsWith("/api/")) requests.push({ method: request.method(), path: new URL(request.url()).pathname }); });
  for (const name of ["board", "login"]) {
    await page.goto((process.env.FRONTEND_TEST_ORIGIN || "http://127.0.0.1:18100") + "/" + name);
    await page.waitForFunction(() => !document.body.innerText.includes("Checking your sign-in status…") && !document.querySelector(".board .list-end [role='status']") && !document.querySelector(".board .gap-body .state-note[role='status']"));
    if (await page.getByRole("alert").count()) throw Error("API error displayed on " + name);
    await page.screenshot({ path: `${output}/${name}.png`, fullPage: true });
  }
} finally { await browser.close(); }
fs.writeFileSync(output + "/checks.json", JSON.stringify({ endpoints, requests, errors }, null, 2));
if (errors.length || requests.some((request) => request.method !== "GET" || /statistics|ballot/.test(request.path))) throw Error("Live read-only verification failed");
console.log("Live API and browser verification passed; no writes or per-task statistics requests.");
