import { chromium } from "@playwright/test";
import { createServer } from "node:http";
import fs from "node:fs";
import { createHash } from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";
const frontendRoot = fileURLToPath(new URL("../", import.meta.url));
const root = path.resolve(frontendRoot, "../prototypes/frontend");
const server = createServer((req, res) => {
  const file = path.join(root, new URL(req.url, "http://local").pathname);
  if (
    !file.startsWith(root) ||
    !fs.existsSync(file) ||
    !fs.statSync(file).isFile()
  ) {
    res.writeHead(404).end();
    return;
  }
  res.setHeader(
    "Content-Type",
    {
      ".html": "text/html",
      ".css": "text/css",
      ".js": "text/javascript",
      ".ttf": "font/ttf",
      ".png": "image/png",
    }[path.extname(file)] || "application/octet-stream",
  );
  res.end(fs.readFileSync(file));
});
const referenceRunning = await fetch("http://127.0.0.1:18765/index.html")
  .then((r) => r.ok)
  .catch(() => false);
if (!referenceRunning)
  await new Promise((r) => server.listen(18765, "127.0.0.1", r));
const sources = [];
for (const file of [
  "index.html",
  "discovery.html",
  "task-detail.html",
  "submit.html",
]) {
  const live = (await (await fetch("http://127.0.0.1:18765/" + file)).text())
    .replaceAll("\r\n", "\n")
    .trim();
  const local = fs
    .readFileSync(path.join(root, file), "utf8")
    .replaceAll("\r\n", "\n")
    .trim();
  if (live !== local) throw Error("18765 does not match " + file);
  sources.push({
    file,
    sha256: createHash("sha256").update(local).digest("hex"),
  });
}
const browser = await chromium.launch({
  channel: process.env.PLAYWRIGHT_CHANNEL || "msedge",
});
const out = path.resolve(
  process.env.VISUAL_OUTPUT ||
    path.join(frontendRoot, "artifacts/visual-review"),
);
fs.mkdirSync(out, { recursive: true });
const ref = await browser.newPage();
await ref.goto("http://127.0.0.1:18765/discovery.html");
const tasks = await ref.evaluate(() =>
  HWPrototype.tasks.map((t) => ({
    ...t,
    revision: 1,
    author: { id: "a", displayName: "Community" },
    taskAttachments: [],
    publishedAt: null,
  })),
);
const entries = await ref.evaluate(() =>
  HWPrototype.worksByTask["concept-library"].map((w) => ({
    ...w,
    artifacts: [{ kind: "text", text: w.text }],
  })),
);
const metrics = [];
const measure = () =>
  Object.fromEntries(
    [
      ".opening",
      ".context-gallery",
      ".hero",
      ".editor-layout",
      ".page-intro",
      ".browse-content",
      ".task-header",
      ".chapter-entrances",
      "#situation",
      ".shared-brief",
    ].flatMap((selector) => {
      const el = document.querySelector(selector);
      if (!el) return [];
      const r = el.getBoundingClientRect();
      return [[selector, { x: r.x, y: r.y, width: r.width, height: r.height }]];
    }),
  );
for (const width of [1440, 390])
  for (const [name, url, actual] of [
    ["home", "index.html", "/"],
    ["discovery", "discovery.html", "/discovery"],
    [
      "detail",
      "task-detail.html?task=concept-library",
      "/tasks/concept-library",
    ],
    ["submit", "submit.html", "/submit"],
  ]) {
    await ref.setViewportSize({ width, height: 1000 });
    await ref.goto("http://127.0.0.1:18765/" + url);
    await ref.waitForTimeout(450);
    await ref.evaluate(() => scrollTo(0, 0));
    await ref.screenshot({
      path: `${out}/${name}-${width}-reference.png`,
      fullPage: true,
    });
    const page = await browser.newPage({ viewport: { width, height: 1000 } });
    await page.addInitScript(()=>sessionStorage.setItem("human-worth-preview-data","off"));
    await page.route("**/api/**", (route) => {
      const p = new URL(route.request().url()).pathname;
      let data =
        p === "/api/me"
          ? {
              account: { id: "a", displayName: "Reader" },
              role: "user",
              csrfToken: "test",
            }
          : p === "/api/tasks"
            ? { items: tasks.slice(0, 4), nextCursor: null }
            : p.endsWith("ballot")
              ? { id: "b", entries, currentVote: null }
              : p.endsWith("voting-eligibility")
                ? { canVote: true }
                : p.endsWith("/concept-library")
                  ? tasks[0]
                  : { items: [], nextCursor: null };
      return route.fulfill({ json: data });
    });
    await page.goto("http://127.0.0.1:18100" + actual);
    await page.waitForTimeout(450);
    await page.evaluate(() => scrollTo(0, 0));
    await page.screenshot({
      path: `${out}/${name}-${width}-${process.env.LABEL || "before"}.png`,
      fullPage: true,
    });
    metrics.push({
      page: name,
      viewport: width,
      reference: await ref.evaluate(measure),
      implementation: await page.evaluate(measure),
    });
    if (name === "detail") {
      await ref.locator('[data-open-chapter="human"]').first().click();
      await page.locator('.chapter-cover[data-side="human"]').click();
      await ref
        .locator(".book-spread")
        .screenshot({ path: out + "/reader-" + width + "-reference.png" });
      await page
        .locator(".book-spread")
        .screenshot({ path: out + "/reader-" + width + "-after.png" });
    }
    await page.close();
  }
const checks = [];
for (const m of metrics) {
  for (const selector of [
    ".opening",
    ".context-gallery",
    ".page-intro",
    ".task-header",
    ".hero",
    ".chapter-entrances",
  ]) {
    const r = m.reference[selector],
      a = m.implementation[selector];
    if (!r) continue;
    if (!a) throw Error("Missing " + m.page + selector);
    for (const key of ["width", "height"]) {
      const delta = Math.abs(a[key] - r[key]);
      checks.push({
        page: m.page,
        viewport: m.viewport,
        selector,
        key,
        delta,
        pass: delta <= 3,
      });
      if (delta > 3)
        throw Error(
          m.page + " " + selector + " " + key + " mismatch: " + delta,
        );
    }
  }
}
fs.writeFileSync(out + "/checks.json", JSON.stringify(checks, null, 2));
fs.writeFileSync(out + "/sources.json", JSON.stringify(sources, null, 2));
fs.writeFileSync(out + "/metrics.json", JSON.stringify(metrics, null, 2));
fs.writeFileSync(
  out + "/index.html",
  '<meta charset="UTF-8"><title>Human Worth 视觉对照</title><style>body{font:16px system-ui;background:#f1ebe4;margin:30px}section{display:grid;grid-template-columns:1fr 1fr;gap:20px}img{width:100%}h2{margin-top:50px}</style><h1>同尺寸视觉对照：左为原型，右为 React</h1><p>使用隔离示例响应，不写入公网。页面长度受实际内容和账号状态影响。</p>' +
    metrics
      .map(
        (m) =>
          "<h2>" +
          m.page +
          " · " +
          m.viewport +
          'px</h2><section><img src="' +
          m.page +
          "-" +
          m.viewport +
          '-reference.png"><img src="' +
          m.page +
          "-" +
          m.viewport +
          '-after.png"></section>',
      )
      .join(""),
);
await browser.close();
if (!referenceRunning) await new Promise((r) => server.close(r));
console.log(out);
