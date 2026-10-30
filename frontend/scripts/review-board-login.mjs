import { chromium } from "@playwright/test";
import { createServer } from "node:http";
import fs from "node:fs";
import path from "node:path";
import data from "./data/design-data.json" with { type: "json" };

const root = path.resolve("../prototypes/frontend"), output = "artifacts/board-login-comparison";
fs.mkdirSync(output, { recursive: true });
const server = createServer((request, response) => {
  const url = new URL(request.url, "http://local");
  if (url.pathname === "/api/me") { response.writeHead(401, { "Content-Type": "application/json" }).end('{"detail":"Unauthorized"}'); return; }
  const file = path.resolve(root, "." + url.pathname);
  if (!file.startsWith(root + path.sep)) { response.writeHead(403).end(); return; }
  try { const reference = path.resolve('tests/fixtures/login-reference', path.basename(file)); const contentFile = ['login.html','login.css','login.js','prototype-nav.css'].includes(path.basename(file)) && fs.existsSync(reference) ? reference : file; response.setHeader("Content-Type", ({ ".html": "text/html", ".css": "text/css", ".js": "text/javascript", ".ttf": "font/ttf", ".png": "image/png" })[path.extname(file)] || "application/octet-stream"); response.end(fs.readFileSync(contentFile)); }
  catch { response.writeHead(404).end(); }
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const browser = await chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "msedge" });
const rows = data.tasks.map((task, index) => ({ ...task, conclusion: ["human", "undetermined", "agent", "undetermined", "human", "undetermined", "agent", "undetermined"][index] }));
const count = (items) => ({ publishedTasks: items.length, humanLeadingTasks: items.filter((task) => task.conclusion === "human").length, agentLeadingTasks: items.filter((task) => task.conclusion === "agent").length, undeterminedTasks: items.filter((task) => task.conclusion === "undetermined").length });
const board = { overall: count(rows), categories: [...new Set(rows.map((task) => task.category))].map((category) => ({ category, summary: count(rows.filter((task) => task.category === category)) })), computedAt: "2026-10-30T00:00:00Z" };
const metrics = [], errors = [];
try {
  for (const width of [1440, 390]) for (const name of ["board", "login"]) for (const source of ["original", "react"]) {
    const page = await browser.newPage({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
    page.on("pageerror", (error) => errors.push(error.message));
    await page.route("**/api/**", (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/me") return route.fulfill({ status: 401, json: { detail: "Unauthorized" } });
      if (url.pathname === "/api/board") return route.fulfill({ json: board });
      const filtered = rows.filter((task) => !url.searchParams.get("category") || task.category === url.searchParams.get("category"));
      return route.fulfill({ json: { items: filtered.slice(0, 3), nextCursor: "3" } });
    });
    await page.goto(source === "original" ? `http://127.0.0.1:${server.address().port}/${name}.html` : `${process.env.FRONTEND_TEST_ORIGIN || "http://127.0.0.1:18100"}/${name}`);
    if (name === "board") await page.locator(".task").first().waitFor();
    else await page.getByRole("status").filter({ hasText: "Sign in with your Google account." }).waitFor();
    await page.evaluate(() => document.fonts.ready);
    const selectors = name === "board" ? [".board", ".hero", ".s1", ".s2", ".s3"] : [".login-card", ".login-card h1"];
    const boxes = {};
    for (const selector of selectors) boxes[selector] = await page.locator(selector).boundingBox();
    metrics.push({ name, width, source, boxes, overflow: await page.evaluate(() => document.documentElement.scrollWidth > innerWidth) });
    await page.screenshot({ path: `${output}/${name}-${width}-${source}.png`, fullPage: true });
    await page.close();
  }
} finally { await browser.close(); server.close(); }
fs.writeFileSync(output + "/checks.json", JSON.stringify({ metrics, errors }, null, 2));
fs.writeFileSync(output + "/index.html", `<!doctype html><html lang="en"><meta charset="utf-8"><title>Board and Login comparison</title><style>body{margin:24px;font:16px sans-serif;background:#eee9e0}section{display:grid;grid-template-columns:1fr 1fr;gap:20px}img{width:100%}h2{grid-column:1/-1}</style><h1>Original design / React with API fixtures</h1>${[1440, 390].flatMap((width) => ["board", "login"].map((name) => `<section><h2>${name} · ${width}px</h2><div><p>Original</p><img src="${name}-${width}-original.png"></div><div><p>React</p><img src="${name}-${width}-react.png"></div></section>`)).join("")}</html>`);
if (errors.length || metrics.some((item) => item.overflow)) throw Error("Visual review failed; inspect checks.json");
console.log("Original and React screenshots saved in " + output + "; no script errors or viewport overflow.");
