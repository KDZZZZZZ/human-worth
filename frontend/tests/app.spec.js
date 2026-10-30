import { test, expect } from "@playwright/test";
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    if (sessionStorage.getItem("human-worth-preview-data") === null)
      sessionStorage.setItem("human-worth-preview-data", "off");
  });
});
const task = {
  id: "t1",
  revision: 1,
  title: "A library is closing.",
  summary: "Write a notice for its readers.",
  description: "The library closes for repairs. Reopens on Friday.",
  category: "Writing",
  author: { id: "u1", displayName: "Reader" },
  taskAttachments: [],
  heat: 1,
  publishedAt: null,
};
const entries = ["human", "agent", "human"].map((side, i) => ({
  id: "e" + i,
  taskId: "t1",
  side,
  title: "Work " + i,
  description: "",
  source: "Source " + i,
  artifacts: [{ kind: "text", text: "Full original work " + i }],
  author: task.author,
  approvedAt: null,
}));
async function setup(
  page,
  { signed = true, admin = false, conflict = false } = {},
) {
  const requests = [];
  let closed = false;
  let vote = null;
  await page.route("**/api/**", async (route) => {
    const req = route.request(),
      url = new URL(req.url()),
      path = url.pathname;
    requests.push({
      path,
      method: req.method(),
      body: req.postDataJSON?.bind(req),
    });
    const respond = (data, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(data),
      });
    if (path === "/api/board") return respond({ overall: { publishedTasks: 7, humanLeadingTasks: 2, agentLeadingTasks: 1, undeterminedTasks: 4 }, categories: [], computedAt: new Date().toISOString() });
    if (path === "/api/me")
      return signed
        ? respond({
            account: task.author,
            role: admin ? "admin" : "user",
            csrfToken: "csrf-test",
          })
        : respond({ detail: "Sign in required" }, 401);
    if (path === "/api/tasks")
      return respond({ items: [task], nextCursor: null });
    if (path === "/api/tasks/t1") return respond(task);
    if (path.endsWith("/entries"))
      return respond({ items: entries, nextCursor: null });
    if (path.endsWith("/comments"))
      return respond({ items: [], nextCursor: null });
    if (path.endsWith("/voting-eligibility"))
      return respond({
        taskId: "t1",
        canVote: !closed,
        permanentlyClosed: closed,
        reason: closed ? "statistics_viewed" : null,
        closedAt: null,
      });
    if (path.endsWith("/ballot"))
      return respond({
        id: "b1",
        taskId: "t1",
        catalogRevision: 1,
        generation: 1,
        entries,
        currentVote: vote,
        expiresAt: "2099-01-01T00:00:00Z",
      });
    if (path.endsWith("/vote")) {
      expect(req.headers()["x-csrf-token"]).toBe("csrf-test");
      expect(req.postDataJSON()).toEqual({
        ballotId: "b1",
        entryId: "e2",
        expectedVoteRevision: 0,
      });
      if (conflict)
        return respond({ detail: "候选已更新", code: "catalog_conflict" }, 409);
      vote = { id: "v1", entryId: "e2", revision: 1 };
      return respond(vote);
    }
    if (path.endsWith("/statistics-access")) {
      expect(req.postDataJSON()).toEqual({ permanentlyCloseVoting: true });
      closed = true;
      return respond(
        { detail: "统计暂时失败", code: "statistics_unavailable" },
        503,
      );
    }
    if (path === "/api/board")
      return respond({
        overall: {
          publishedTasks: 1,
          humanLeadingTasks: 0,
          agentLeadingTasks: 0,
          undeterminedTasks: 1,
        },
        categories: [],
        computedAt: "",
      });
    if (path === "/api/admin/dashboard")
      return respond({
        pendingTaskReviews: 1,
        pendingEntryReviews: 0,
        openReports: 0,
        activeRuns: 0,
        failedRuns: 0,
      });
    return respond({ items: [], nextCursor: null });
  });
  return requests;
}
async function chooseLastHuman(page) {
  await page
    .getByRole("button", { name: "Chapter I Human Work", exact: false })
    .click();
  await page.getByRole("button", { name: "Next →", exact: true }).click();
  await page.getByRole("button", { name: "Choose this work", exact: true }).click();
}
test("discovery loads real endpoint without fetching per-task statistics", async ({
  page,
}) => {
  const requests = await setup(page);
  await page.goto("/discovery");
  await expect(page.getByRole("heading", { name: task.title })).toBeVisible();
  expect(requests.some((r) => /statistics|ballot/.test(r.path))).toBe(false);
});
test("all candidates are selectable and vote writes CSRF and ballot revision", async ({
  page,
}) => {
  const requests = await setup(page);
  await page.goto("/tasks/t1");
  await expect(page.locator(".chapter-cover")).toHaveCount(2);
  await chooseLastHuman(page);
  expect(requests.some((r) => r.method === "PUT")).toBe(false);
  await page.getByRole("button", { name: "Confirm my choice" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Choice confirmed" }),
  ).toBeVisible();
});
test("statistics requires confirmation and failed response still preserves permanent closure", async ({
  page,
}) => {
  const requests = await setup(page);
  await page.goto("/tasks/t1");
  await expect(page.locator(".chapter-cover")).toHaveCount(2);
  expect(requests.some((r) => r.path.endsWith("statistics-access"))).toBe(
    false,
  );
  await page.getByRole("button", { name: "View statistics…" }).click();
  expect(requests.some((r) => r.path.endsWith("statistics-access"))).toBe(
    false,
  );
  await page.getByRole("button", { name: "Close voting & view statistics" }).click();
  await expect(
    page.getByText("Voting is permanently closed for this task.", { exact: false }),
  ).toBeVisible();
  await expect(page.locator(".chapter-cover")).toHaveCount(2);
  await expect(page.getByRole("link", { name: "Add a work ↗" })).toBeVisible();
});
test("conflict invalidates old ballot and requires reload", async ({
  page,
}) => {
  await setup(page, { conflict: true });
  await page.goto("/tasks/t1");
  await chooseLastHuman(page);
  await page.getByRole("button", { name: "Confirm my choice" }).click();
  await expect(page.getByText("候选已更新")).toBeVisible();
  await expect(page.locator(".chapter-cover")).toHaveCount(2);
  await page.getByRole("button", { name: "Reload all works" }).click();
  await expect(page.locator(".chapter-cover")).toHaveCount(2);
});
test("desktop and narrow pages render without script errors or horizontal overflow", async ({
  page,
}) => {
  await setup(page);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    for (const path of ["/", "/discovery", "/tasks/t1", "/submit"]) {
      await page.goto(path);
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      await page.waitForTimeout(100);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    }
  }
  expect(errors).toEqual([]);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/tasks/t1");
  await expect(page.locator(".chapter-cover")).toHaveCount(2);
  await page
    .getByRole("button", { name: "Chapter I Human Work", exact: false })
    .click();
  await page.screenshot({
    path: "test-results/task-reader-desktop.png",
    fullPage: true,
  });
});
test("task creation retries the original idempotency key after lost response", async ({
  page,
}) => {
  await setup(page);
  let calls = [];
  await page.route("**/api/tasks", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    const req = route.request();
    calls.push({
      key: req.headers()["idempotency-key"],
      body: req.postDataJSON(),
    });
    if (calls.length === 1)
      return route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ detail: "响应暂时丢失" }),
      });
    return route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({
        kind: "task",
        id: "new-task",
        revision: 1,
        state: "draft",
        content: req.postDataJSON(),
        rejectionReason: null,
      }),
    });
  });
  await page.goto("/submit");
  await page.getByLabel("Task title").fill("A new task");
  await page.getByRole("button", { name: "Save draft" }).click();
  await expect(page.getByText("响应暂时丢失")).toBeVisible();
  await page.getByRole("button", { name: "Save draft" }).click();
  await expect(page.getByText("Draft saved.")).toBeVisible();
  expect(calls).toHaveLength(2);
  expect(calls[0]).toEqual(calls[1]);
});
test("submission editor preserves complete work in preview and blocks missing opposite side", async ({
  page,
}) => {
  await setup(page);
  await page.goto("/submit");
  await page.getByLabel("Task title").fill("A new task");
  await page.getByLabel("Short introduction").fill("An introduction");
  await page.getByLabel("Instructions & requirements").fill("Write a letter");
  await page.getByRole("button", { name: "+ Add human work" }).click();
  await page.getByLabel("Work title").fill("A letter");
  await page.getByLabel("Author / source").fill("Me");
  await page
    .getByLabel("The work", { exact: true })
    .fill("Complete original text, including its ending.");
  await page.getByRole("radio", { name: "Do not allow", exact: true }).check();
  await page.getByRole("button", { name: "Keep in this draft" }).click();
  await page.getByRole("button", { name: "Preview the submission ↗" }).click();
  await expect(
    page
      .locator(".review-preview")
      .getByText("Complete original text, including its ending.", {
        exact: true,
      }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Close preview", exact: true }).click();
  await page.getByRole("button", { name: "Submit for review" }).click();
  await expect(
    page.getByText(
      "Complete the task information and add at least one Human work and one Agent work before submitting for review.",
    ),
  ).toBeVisible();
});
test("multipart upload resumes confirmed offset after lost response and only references ready assets", async ({
  page,
}) => {
  await setup(page);
  const size = 5242883;
  let offset = 0,
    failed = false;
  const chunks = [];
  await page.route("**/api/me/assets/upload-sessions", (route) => {
    const data = route.request().postDataJSON();
    expect(data.expectedBytes).toBe(size);
    expect(data.sha256).toMatch(/^[a-f0-9]{64}$/);
    return route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({
        asset: { id: "asset1" },
        uploadSession: { id: "upload1" },
      }),
    });
  });
  await page.route("**/api/me/upload-sessions/upload1", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ receivedBytes: offset, state: "active" }),
    }),
  );
  await page.route(
    "**/api/me/assets/upload-sessions/upload1/content",
    (route) => {
      const req = route.request();
      chunks.push(req.headers()["content-range"]);
      expect(req.headers()["x-csrf-token"]).toBe("csrf-test");
      offset += req.postDataBuffer().length;
      if (!failed) {
        failed = true;
        return route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ detail: "上传响应丢失" }),
        });
      }
      return route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ nextOffset: offset }),
      });
    },
  );
  await page.route("**/api/me/assets/asset1/complete", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        id: "asset1",
        state: "ready",
        filename: "large.txt",
      }),
    }),
  );
  await page.goto("/submit");
  await page.getByRole("button", { name: "+ Add human work" }).click();
  await page.getByLabel("Files", { exact: true }).setInputFiles({
    name: "large.txt",
    mimeType: "text/plain",
    buffer: Buffer.alloc(size, 65),
  });
  await expect(page.getByText("上传响应丢失")).toBeVisible();
  await page.getByRole("button", { name: "Check progress & resume upload" }).click();
  await expect(
    page.getByText("Uploaded file asset1", { exact: false }),
  ).toBeVisible();
  expect(chunks).toEqual([
    "bytes 0-5242879/5242883",
    "bytes 5242880-5242882/5242883",
  ]);
});
test("a user can append an Agent work independently without resubmitting the original task", async ({
  page,
}) => {
  await setup(page);
  let submitted = null;
  await page.route("**/api/tasks/t1/entries", (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    submitted = route.request().postDataJSON();
    expect(submitted.side).toBe("agent");
    expect(submitted.agentConfiguration.model).toBe("test-model");
    expect(submitted.permissions.cloudUse).toBe(false);
    expect(submitted).not.toHaveProperty("authorId");
    return route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({
        kind: "entry",
        id: "new-entry",
        revision: 1,
        state: "pending_review",
        content: submitted,
        taskId: "t1",
      }),
    });
  });
  await page.goto("/submit?task=t1");
  await page.getByLabel("Work type").selectOption("agent");
  await page.getByLabel("Work title").fill("Another approach");
  await page.getByLabel("Author / source").fill("Generated by me");
  await page.getByLabel("The work", { exact: true }).fill("The complete Agent work.");
  await page.getByLabel("Model", { exact: true }).fill("test-model");
  await page.getByRole("radio", { name: "Do not allow", exact: true }).check();
  await page.getByRole("button", { name: "Keep in this draft" }).click();
  await page.getByRole("button", { name: "Submit for review" }).click();
  await expect(page.getByText("Submitted for review. Your submission will be shared after review.")).toBeVisible();
  expect(submitted.side).toBe("agent");
});

test("Home reads featured tasks from the API and links to Discovery", async ({
  page,
}) => {
  const requests = await setup(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "Who made it?What does it matter?",
  );
  await expect(page.locator(".example")).toHaveCount(1);
  await expect(page.locator(".example")).toContainText(task.summary);
  await expect(page.locator(".example .document")).toContainText(task.description);
  expect(requests.some((r) => r.path === "/api/tasks")).toBe(true);
  await page
    .getByRole("link", { name: "Browse Tasks", exact: true })
    .first()
    .click();
  await expect(page).toHaveURL(/\/discovery$/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Discovery");
});
test("Board reads its API and unimplemented administration routes stay unavailable", async ({
  page,
}) => {
  const requests = await setup(page);
  await page.goto("/");
  await page.getByRole("link", { name: "Board", exact: true }).click();
  await expect(page).toHaveURL(/\/board\?shown=1$/);
  await expect(page.getByRole("heading", { name: "The Gap", exact: true })).toBeVisible();
  for (const path of [
    "/admin",
    "/me/credentials",
  ]) {
    await page.goto(path);
    await expect(
      page.getByRole("heading", { name: "Page not found" }),
    ).toBeVisible();
  }
  expect(requests.some((r) => r.path.startsWith("/api/admin"))).toBe(false);
});
test("anonymous visitors can inspect Submit design but cannot write a draft", async ({
  page,
}) => {
  const requests = await setup(page, { signed: false });
  await page.goto("/submit");
  await expect(page.getByRole("heading", { level: 1 })).toContainText(
    "Create a task.",
  );
  await page.getByRole("button", { name: "Save draft" }).click();
  await expect(
    page.getByText("Sign in to save or submit your work."),
  ).toBeVisible();
  expect(requests.some((r) => r.method === "POST")).toBe(false);
});

test("shared attachments require an explicit cloud-use choice before saving", async ({
  page,
}) => {
  await setup(page);
  let saved = null;
  const respond = (route, data, status = 200) =>
    route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(data),
    });
  await page.route("**/api/me/assets/upload-sessions", (r) =>
    respond(
      r,
      { asset: { id: "shared1" }, uploadSession: { id: "upload-shared" } },
      201,
    ),
  );
  await page.route("**/api/me/upload-sessions/upload-shared", (r) =>
    respond(r, { receivedBytes: 0 }),
  );
  await page.route(
    "**/api/me/assets/upload-sessions/upload-shared/content",
    (r) => respond(r, { nextOffset: 3 }),
  );
  await page.route("**/api/me/assets/shared1/complete", (r) =>
    respond(r, { id: "shared1", state: "ready", filename: "shared.txt" }),
  );
  await page.route("**/api/tasks", (r) => {
    if (r.request().method() !== "POST") return r.fallback();
    saved = r.request().postDataJSON();
    return respond(
      r,
      { id: "new-task", revision: 1, state: "draft", content: saved },
      201,
    );
  });
  await page.goto("/submit");
  await page.getByLabel("Task title").fill("A task with shared material");
  await page.getByLabel("Choose shared files").setInputFiles({
    name: "shared.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("abc"),
  });
  await expect(page.getByText("File uploaded: shared.txt")).toBeVisible();
  await page.getByRole("button", { name: "Save draft" }).click();
  await expect(
    page.getByText("Choose a permission for every task attachment."),
  ).toBeVisible();
  expect(saved).toBe(null);
  await page
    .getByLabel("Allow this attachment in cloud challenges?")
    .selectOption("false");
  await page.getByRole("button", { name: "Save draft" }).click();
  await expect(page.getByText("Draft saved.")).toBeVisible();
  expect(saved.taskAttachments).toEqual([
    { assetId: "shared1", cloudUseAllowed: false },
  ]);
});

test("Discovery keeps the designed empty state and reset action when the API has no tasks", async ({
  page,
}) => {
  await setup(page);
  await page.route("**/api/tasks?*", (r) =>
    r.fulfill({ json: { items: [], nextCursor: null } }),
  );
  await page.goto("/discovery?q=no-match");
  await expect(
    page.getByRole("heading", { name: "No tasks found." }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Reset search and category" }),
  ).toHaveAttribute("href", "/discovery");
});

test("all four pages share the Home header dimensions, typography and mobile menu", async ({
  page,
}) => {
  await setup(page);
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    let reference;
    for (const route of ["/", "/discovery", "/tasks/t1", "/submit"]) {
      await page.goto(route);
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      const current = await page.locator(".global-header").evaluate((el) => {
        const nav = el.querySelector("nav"),
          word = el.querySelector(".global-wordmark"),
          links = el.querySelector(".global-links");
        const r = el.getBoundingClientRect();
        return {
          height: r.height,
          width: r.width,
          padding: getComputedStyle(nav).padding,
          font: getComputedStyle(word).font,
          fontSize: getComputedStyle(links).fontSize,
          gap: getComputedStyle(links).gap,
        };
      });
      reference ??= current;
      expect(current).toEqual(reference);
      if (width === 390) {
        await page.getByRole("button", { name: "Menu", exact: true }).click();
        await expect(
          page.getByRole("link", { name: "Home", exact: true }),
        ).toBeVisible();
        await expect(
          page.getByRole("link", { name: "Board", exact: true }),
        ).toBeVisible();
      }
    }
  }
});
test("old preview flags cannot bypass the API or fill a new draft", async ({ page }) => {
  await page.addInitScript(() => {
    sessionStorage.setItem("human-worth-preview-data", "on");
    sessionStorage.setItem("human-worth-design-preview-v1", JSON.stringify({ signed: true }));
  });
  const requests = await setup(page, { signed: false });
  await page.goto("/submit");
  await expect(page.getByRole("link", { name: "Sign in", exact: true })).toBeVisible();
  await expect(page.getByLabel("Task title")).toHaveValue("");
  expect(requests.some(r => r.path === "/api/me")).toBe(true);
  await page.getByRole("button", { name: "Save draft" }).click();
  await expect(page.getByText("Sign in to save or submit your work.")).toBeVisible();
  expect(requests.some(r => r.method === "POST")).toBe(false);
});
