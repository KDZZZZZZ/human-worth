import { mockDesignApi } from "./fixtures/design-api.js";
import { test, expect } from "@playwright/test";
import data from "../scripts/data/design-data.json" with { type: "json" };
import { mkdirSync } from "node:fs";

test.beforeEach(async ({ page }) => {
  await mockDesignApi(page);
  await page.addInitScript(() => {
    sessionStorage.setItem("human-worth-preview-data", "on");
    addEventListener("pageswap", (event) => sessionStorage.setItem("transition-swap", String(!!event.viewTransition)));
    const animate = Element.prototype.animate;
    Element.prototype.animate = function (...args) {
      if (this.id === "main") sessionStorage.setItem("page-exit", "played");
      return animate.apply(this, args);
    };
  });
  mkdirSync("artifacts/interaction-fixes", { recursive: true });
});

test("category index survives filtering, empty search, reload and switching categories", async ({ page }) => {
  const categories = ["All tasks", ...new Set(data.tasks.map((task) => task.category).filter(Boolean))];
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/discovery?category=Software");
    await expect(page.locator(".category-index a")).toHaveText(categories);
    await expect(page.locator(".task-item")).toHaveCount(2);
    await page.locator(".category-index").getByRole("link", { name: "Communication", exact: true }).click();
    await expect(page.locator(".category-index a")).toHaveText(categories);
    await expect(page.locator(".task-item").first()).toContainText("A library is closing.");
    await page.reload();
    await expect(page.locator(".category-index a")).toHaveText(categories);
    await page.getByPlaceholder("Search tasks").fill("no-matching-example-999");
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.getByRole("heading", { name: "No tasks found." })).toBeVisible();
    await expect(page.locator(".category-index a")).toHaveText(categories);
    await page.screenshot({ path: `artifacts/interaction-fixes/categories-${width}.png`, fullPage: true });
  }
});

test("report action stays compact and voting actions have space on desktop and mobile", async ({ page }) => {
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/tasks/concept-library");
    await page.getByRole("button", { name: "Report task", exact: true }).click();
    const report = page.getByRole("button", { name: "Submit report", exact: true });
    const box = await report.boundingBox();
    expect(box.height).toBeLessThanOrEqual(40);
    expect(box.width).toBeLessThan(180);
    await page.locator("#situation").screenshot({ path: `artifacts/interaction-fixes/report-${width}.png` });
    const confirm = await page.getByRole("button", { name: "Confirm my choice", exact: true }).boundingBox();
    const cancel = await page.getByRole("button", { name: "Cancel selection", exact: true }).boundingBox();
    expect(cancel.x - (confirm.x + confirm.width) >= 23 || cancel.y - (confirm.y + confirm.height) >= 11).toBe(true);
    await page.locator(".current-choice").screenshot({ path: `artifacts/interaction-fixes/choice-${width}.png` });
  }
});

test("navigation scales in place and cross-page transitions run while reduced motion opts out", async ({ page }) => {
  await page.goto("/");
  await page.waitForTimeout(500);
  const link = page.getByRole("link", { name: "Discovery", exact: true });
  const initial = await link.boundingBox();
  await link.hover();
  await page.waitForTimeout(260);
  const hoveredLink = await link.boundingBox();
  expect(hoveredLink.width / initial.width).toBeCloseTo(1.04, 2);
  expect(hoveredLink.y + hoveredLink.height / 2).toBeCloseTo(initial.y + initial.height / 2, 1);
  await link.click();
  await expect(page.getByRole("heading", { name: "Discovery", exact: true })).toBeVisible();
  expect(await page.evaluate(() => sessionStorage.getItem("page-exit"))).toBe("played");
  await page.waitForTimeout(500);
  const search = page.getByRole("button", { name: "Search", exact: true });
  const rest = await search.boundingBox();
  await search.hover();
  await page.waitForTimeout(220);
  const hoveredButton = await search.boundingBox();
  expect(hoveredButton.width).toBeCloseTo(rest.width, 1);
  expect(rest.y - hoveredButton.y).toBeCloseTo(2, 1);
  await page.goBack();
  await expect(page.getByRole("heading", { level: 1 })).toContainText("Who made it?");
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.evaluate(() => sessionStorage.removeItem("page-exit"));
  await page.waitForTimeout(500);
  await page.getByRole("link", { name: "Discovery", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Discovery", exact: true })).toBeVisible();
  expect(await page.evaluate(() => sessionStorage.getItem("page-exit"))).toBeNull();
});

test("cancelling navigation keeps the unsaved draft visible and editable", async ({ page }) => {
  await page.goto("/submit");
  await page.getByLabel("Task title", { exact: true }).fill("Keep my unsaved draft");
  const prompt = page.waitForEvent("dialog");
  await page.getByRole("link", { name: "Discovery", exact: true }).click({ noWaitAfter: true });
  const dialog = await prompt;
  expect(dialog.type()).toBe("beforeunload");
  await dialog.dismiss();
  await expect(page).toHaveURL(/\/submit$/);
  await expect(page.getByLabel("Task title", { exact: true })).toHaveValue("Keep my unsaved draft");
  await expect.poll(() => page.locator("#main").evaluate((element) => getComputedStyle(element).opacity)).toBe("1");
});
