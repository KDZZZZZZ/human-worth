import { test, expect } from "@playwright/test";
import data from "../scripts/data/design-data.json" with { type: "json" };
import { mkdirSync } from "node:fs";

const account = { id: "reader-1", displayName: "API Reader" };
const session = { account, role: "user", csrfToken: "server-csrf" };
const totals = (items) => ({ publishedTasks: items.length, humanLeadingTasks: items.filter((item) => item.conclusion === "human").length,
  undeterminedTasks: items.filter((item) => item.conclusion === "undetermined").length, agentLeadingTasks: items.filter((item) => item.conclusion === "agent").length });
const tasks = data.tasks.map((task, index) => ({ ...task, conclusion: ["human", "undetermined", "agent", "undetermined", "human", "undetermined", "agent", "undetermined"][index] }));
const categories = [...new Set(tasks.map((task) => task.category))];
const board = { overall: totals(tasks), categories: categories.map((category) => ({ category, summary: totals(tasks.filter((task) => task.category === category)) })), computedAt: "2026-10-30T00:00:00Z" };
const respond = (route, body, status = 200) => route.fulfill({ status, json: body });

async function fixture(page, { signed = false, empty = false, failMore = false, failBoard = false } = {}) {
  const requests = [];
  let failedMore = false, failedBoard = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request(), url = new URL(request.url());
    requests.push({ path: url.pathname, query: url.search, method: request.method() });
    if (url.pathname === "/api/me") return signed ? respond(route, session) : respond(route, { detail: "Unauthorized" }, 401);
    if (url.pathname === "/api/board") {
      if (failBoard && !failedBoard) { failedBoard = true; return respond(route, { detail: "Board unavailable" }, 503); }
      return respond(route, empty ? { overall: totals([]), categories: [], computedAt: board.computedAt } : board);
    }
    if (url.pathname === "/api/tasks") {
      expect(url.searchParams.get("order")).toBe("hot");
      if (failMore && url.searchParams.has("cursor") && !failedMore) { failedMore = true; return respond(route, { detail: "More tasks unavailable" }, 503); }
      const filtered = empty ? [] : tasks.filter((task) => !url.searchParams.get("category") || task.category === url.searchParams.get("category"));
      const start = Number(url.searchParams.get("cursor") || 0), end = start + Number(url.searchParams.get("limit") || 3);
      return respond(route, { items: filtered.slice(start, end), nextCursor: end < filtered.length ? String(end) : null });
    }
    const task = tasks.find((task) => url.pathname === "/api/tasks/" + task.id);
    if (task) return respond(route, { ...task, taskAttachments: [] });
    if (url.pathname.endsWith("/entries") || url.pathname.endsWith("/comments")) return respond(route, { items: [], nextCursor: null });
    return respond(route, { detail: "Unexpected request" }, 404);
  });
  return requests;
}

test("Board renders aggregate counts and real hot pagination, preserves rows after failure, and filters only discussions", async ({ page }) => {
  const requests = await fixture(page, { failMore: true, failBoard: true });
  await page.goto("/board");
  await expect(page.locator(".s1").getByRole("alert")).toContainText("Board unavailable");
  await expect(page.locator(".board .task")).toHaveCount(3);
  await page.locator(".s1").getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.locator(".s1 .total")).toContainText(String(tasks.length));
  await page.getByRole("button", { name: "Load more", exact: true }).click();
  await expect(page.locator(".s3").getByRole("alert")).toContainText("More tasks unavailable");
  await expect(page.locator(".board .task")).toHaveCount(3);
  await page.locator(".s3").getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.locator(".board .task")).toHaveCount(6);
  await page.locator(".s2").getByRole("button", { name: "Software", exact: true }).click();
  await expect(page.getByLabel("Filter Most Discussed")).toHaveValue("Software");
  await expect(page.locator(".board .task")).toHaveCount(2);
  await expect(page.locator(".s1 .total")).toContainText(String(tasks.length));
  await expect(page.locator(".s2 tbody tr")).toHaveCount(categories.length);
  await page.getByRole("link", { name: "View task", exact: false }).first().click();
  await expect(page.getByRole("link", { name: "← Back to Board", exact: true })).toHaveAttribute("href", /\/board\?category=Software/);
  await page.getByRole("link", { name: "← Back to Board", exact: true }).click();
  await expect(page.getByLabel("Filter Most Discussed")).toHaveValue("Software");
  expect(requests.some((request) => /statistics|ballot|voting-eligibility/.test(request.path))).toBe(false);
  expect(requests.every((request) => request.method === "GET")).toBe(true);
});

test("Board zero counts are zero, and a task return cannot navigate outside the approved list pages", async ({ page }) => {
  await fixture(page, { empty: true });
  await page.goto("/board.html");
  await expect(page.locator(".s1 .dash")).toHaveText(["0", "0", "0"]);
  await expect(page.getByText("No discussed tasks in this category.", { exact: true })).toBeVisible();
  for (const unsafe of ["https://evil.example/board", "//evil.example/board", "/api/auth/logout", "/admin", "javascript:alert(1)"]) {
    await page.goto("/tasks/concept-library?return=" + encodeURIComponent(unsafe));
    await expect(page.getByRole("link", { name: "← Back to Discovery", exact: true })).toHaveAttribute("href", "/discovery");
  }
});

test("Login checks real session, handles cancelled callback and prevents HTTP OAuth", async ({ page }) => {
  const requests = await fixture(page);
  await page.goto("/?login=cancelled");
  await expect(page).toHaveTitle("Sign in · Human Worth");
  await expect(page.getByRole("status")).toContainText("Google sign-in was cancelled.");
  await expect(page.getByRole("status")).toContainText("this local preview cannot complete sign-in");
  const google = page.getByRole("link", { name: "Continue with Google", exact: true });
  await expect(google).toBeVisible();
  await expect(google).toBeDisabled();
  await expect(google).not.toHaveAttribute("href");
  await google.evaluate((element) => element.click());
  await expect(page).toHaveURL(/login=cancelled/);
  await page.getByRole("link", { name: "Browse without signing in" }).click();
  await expect(page).toHaveURL(/\/discovery$/);
  expect(requests.some((request) => request.path === "/api/auth/google")).toBe(false);
});

test("Login logout uses server CSRF, never confirms failed logout, retries session and handles expiry", async ({ page }) => {
  let status = 200, logoutStatus = 503, reads = 0;
  await page.route("**/api/**", (route) => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (path === "/api/me") { reads++; return respond(route, status === 200 ? session : { detail: "Unavailable" }, status); }
    if (path === "/api/auth/logout") {
      expect(request.method()).toBe("POST");
      expect(request.headers()["x-csrf-token"]).toBe("server-csrf");
      return logoutStatus === 204 ? route.fulfill({ status: 204 }) : respond(route, { detail: "Not confirmed" }, logoutStatus);
    }
    return respond(route, {}, 404);
  });
  await page.goto("/login");
  await expect(page.getByText("API Reader", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => sessionStorage.getItem("human-worth:session"))).not.toContain("server-csrf");
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Sign-out was not confirmed");
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(page.getByRole("status")).toHaveText("You’re signed in.");
  expect(reads).toBeGreaterThan(1);
  logoutStatus = 401;
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Your session has expired");
  expect(await page.evaluate(() => sessionStorage.getItem("human-worth:session"))).toBeNull();
  status = 200;
  await page.reload();
  await expect(page.getByText("API Reader", { exact: true })).toBeVisible();
  logoutStatus = 204;
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("You’re signed out.");
});

test("Login service errors and malformed sessions stay unauthenticated and can recover", async ({ page }) => {
  let reply = { status: 502, data: { detail: "Bad Gateway" } };
  await page.route("**/api/me", (route) => respond(route, reply.data, reply.status));
  await page.goto("/login");
  await expect(page.getByRole("status")).toHaveText("We couldn’t check your account. Please try again.");
  await expect(page.getByRole("link", { name: "Continue with Google", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Continue with Google", exact: true })).toBeDisabled();
  reply = { status: 200, data: { account, role: "admin" } };
  await page.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toHaveCount(0);
  reply = { status: 403, data: { detail: "Forbidden" } };
  await page.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("This account cannot sign in");
  reply = { status: 404, data: { detail: "Not available" } };
  await page.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Sign-in is not available in this preview");
  reply = { status: 401, data: { detail: "Unauthorized" } };
  await page.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Sign in with your Google account.");
});

test("HTTPS Google action is a top-level API navigation, with no client token or callback fabrication", async ({ page }) => {
  let authRequest;
  await page.route("https://frontend.test/**", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.pathname === "/api/me") return respond(route, { detail: "Unauthorized" }, 401);
    if (url.pathname === "/api/auth/google") {
      authRequest = { type: request.resourceType(), method: request.method(), url: url.href };
      return route.fulfill({ contentType: "text/html", body: "<h1>OAuth server reached</h1>" });
    }
    const response = await route.fetch({ url: (process.env.FRONTEND_TEST_ORIGIN || "http://127.0.0.1:18100") + url.pathname + url.search });
    return route.fulfill({ response });
  });
  await page.goto("https://frontend.test/login");
  await page.getByRole("link", { name: "Continue with Google", exact: true }).click();
  await expect(page.getByRole("heading")).toHaveText("OAuth server reached");
  expect(authRequest).toEqual({ type: "document", method: "GET", url: "https://frontend.test/api/auth/google" });
});

test("Board and Login match the design composition without overflow on desktop and mobile", async ({ page }) => {
  await fixture(page);
  await page.emulateMedia({ reducedMotion: "reduce" });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  mkdirSync("artifacts/board-login", { recursive: true });
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    for (const name of ["board", "login"]) {
      await page.goto("/" + name);
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      if (name === "board") await expect(page.locator(".board .task")).toHaveCount(3);
      else await expect(page.getByRole("status")).toContainText("Sign in with your Google account.");
      await page.evaluate(() => document.fonts.ready);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: `artifacts/board-login/${name}-${width}.png`, fullPage: true });
    }
  }
  expect(errors).toEqual([]);
});
