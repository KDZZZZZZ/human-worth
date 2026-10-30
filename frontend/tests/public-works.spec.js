import { test, expect } from "@playwright/test";

async function setup(page, { closed = false, signed = true, eligibilityFails = false, statisticsFails = false } = {}) {
  const requests = [];
  const entries = Array.from({ length: 4 }, (_, i) => ({
    id: `e${i}`, taskId: "reading", side: i % 2 ? "agent" : "human",
    title: `Public work ${i}`, source: "Original author",
    artifacts: [{ kind: "text", text: `Full approved text ${i}` }],
  }));
  let failNextPage = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname;
    requests.push({ path, method: request.method(), cursor: url.searchParams.get("cursor") });
    const reply = (json, status = 200) => route.fulfill({ status, json });
    if (path === "/api/me") return signed
      ? reply({ account: { id: "reader", displayName: "Reader" }, role: "user", csrfToken: "test-csrf" })
      : reply({ detail: "Anonymous" }, 401);
    if (path === "/api/tasks/reading") return reply({
      id: "reading", title: "Read every work", summary: "Public reading",
      description: "Compare approaches", taskAttachments: [],
    });
    if (path.endsWith("/entries")) {
      const start = Number(url.searchParams.get("cursor") || 0);
      if (start && failNextPage) { failNextPage = false; return reply({ detail: "Works temporarily unavailable" }, 503); }
      return reply({ items: entries.slice(start, start + 2), nextCursor: start + 2 < entries.length ? String(start + 2) : null });
    }
    if (path.endsWith("/voting-eligibility")) return eligibilityFails
      ? reply({ detail: "Eligibility temporarily unavailable" }, 503)
      : reply({ canVote: !closed, permanentlyClosed: closed });
    if (path.endsWith("/ballot")) return closed
      ? reply({ detail: "Voting permanently closed" }, 403)
      : reply({ id: "ballot", entries, currentVote: null });
    if (path.endsWith("/statistics-access")) {
      expect(request.method()).toBe("POST");
      expect(request.postDataJSON()).toEqual({ permanentlyCloseVoting: true });
      closed = true;
      return statisticsFails ? reply({ detail: "Statistics temporarily unavailable" }, 503)
        : reply({ totalVotes: 0, humanVotes: 0, agentVotes: 0, entryVotes: [] });
    }
    if (path.endsWith("/comments")) return reply({ items: [], nextCursor: null });
    return reply({ detail: "Unexpected request" }, 404);
  });
  return {
    requests,
    addWork: () => entries.push({ ...entries[0], id: "extra", title: "New approved work", artifacts: [{ kind: "text", text: "New approved text" }] }),
    signIn: (value) => { signed = value; },
    failPage: () => { failNextPage = true; },
  };
}

async function readAll(page, extra = false) {
  await expect(page.locator(".chapter-cover")).toHaveCount(2);
  await page.getByRole("button", { name: "Chapter I Human Work", exact: false }).click();
  while (await page.getByRole("button", { name: "← Previous", exact: true }).isEnabled()) {
    await page.getByRole("button", { name: "← Previous", exact: true }).click();
  }
  await expect(page.locator("#reader-content")).toHaveText("Full approved text 0");
  await page.getByRole("button", { name: "Next →", exact: true }).click();
  await expect(page.locator("#reader-content")).toHaveText("Full approved text 2");
  if (extra) {
    await page.getByRole("button", { name: "Next →", exact: true }).click();
    await expect(page.locator("#reader-content")).toHaveText("New approved text");
  }
  await page.getByRole("button", { name: "Agent Work", exact: true }).click();
  while (await page.getByRole("button", { name: "← Previous", exact: true }).isEnabled()) {
    await page.getByRole("button", { name: "← Previous", exact: true }).click();
  }
  await expect(page.locator("#reader-content")).toHaveText("Full approved text 1");
  await page.getByRole("button", { name: "Next →", exact: true }).click();
  await expect(page.locator("#reader-content")).toHaveText("Full approved text 3");
  await page.getByRole("button", { name: "← Back to Perspectives", exact: true }).click();
}

for (const statisticsFails of [false, true]) {
  test(`reading survives statistics closure, refresh and reload (statistics fails: ${statisticsFails})`, async ({ page }) => {
    const fixture = await setup(page, { statisticsFails });
    await page.goto("/tasks/reading");
    await readAll(page);
    await page.getByRole("button", { name: "View statistics…" }).click();
    await page.getByRole("button", { name: "Close voting & view statistics", exact: true }).click();
    await expect(page.getByText("Voting is permanently closed", { exact: false }).first()).toBeVisible();
    await readAll(page);
    const ballotCount = fixture.requests.filter((r) => r.path.endsWith("/ballot")).length;
    for (const action of [() => page.reload(), () => page.getByRole("button", { name: "Reload all works" }).click()]) {
      await action();
      await readAll(page);
      await expect(page.getByRole("button", { name: "Choose this work" })).toHaveCount(0);
      await expect(page.getByRole("button", { name: "Confirm my choice" })).toHaveCount(0);
      await expect(page.getByLabel("Your comment")).toBeEditable();
      await expect(page.getByRole("link", { name: "Add a work ↗" })).toHaveAttribute("href", "/submit?task=reading");
    }
    expect(fixture.requests.filter((r) => r.path.endsWith("/ballot"))).toHaveLength(ballotCount);
    expect(fixture.requests.filter((r) => /statistics/.test(r.path))).toHaveLength(1);
    expect(fixture.requests.filter((r) => r.path.endsWith("/vote"))).toHaveLength(0);
  });
}

test("permanently closed and anonymous readers see the same paginated works; login and new works do not restore voting", async ({ page }) => {
  const fixture = await setup(page, { closed: true });
  await page.goto("/tasks/reading");
  await readAll(page);
  fixture.signIn(false);
  await page.reload();
  await readAll(page);
  fixture.signIn(true);
  fixture.addWork();
  await page.reload();
  await readAll(page, true);
  await page.getByRole("button", { name: "Reload all works" }).click();
  await expect(page.locator(".chapter-cover").first()).toContainText("3");
  expect(fixture.requests.some((r) => /ballot|statistics|\/vote$/.test(r.path))).toBe(false);
  expect(fixture.requests.some((r) => r.cursor === "4")).toBe(true);
});

test("eligibility failure cannot block public reading or reload", async ({ page }) => {
  const fixture = await setup(page, { eligibilityFails: true });
  await page.goto("/tasks/reading");
  await expect(page.getByText("Eligibility temporarily unavailable")).toBeVisible();
  await readAll(page);
  await page.getByRole("button", { name: "Reload all works" }).click();
  await readAll(page);
  expect(fixture.requests.some((r) => /ballot|statistics|\/vote$/.test(r.path))).toBe(false);
});

test("failed public pagination preserves existing reading and can retry without voting endpoints", async ({ page }) => {
  const fixture = await setup(page, { closed: true });
  await page.goto("/tasks/reading");
  await readAll(page);
  fixture.failPage();
  await page.getByRole("button", { name: "Reload all works" }).click();
  await expect(page.getByText("Works temporarily unavailable")).toBeVisible();
  await readAll(page);
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByText("Works temporarily unavailable")).toHaveCount(0);
  await readAll(page);
  expect(fixture.requests.some((r) => /ballot|statistics|\/vote$/.test(r.path))).toBe(false);
});
