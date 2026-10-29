import { mockDesignApi } from "./fixtures/design-api.js";
import { test, expect } from "@playwright/test";
import { readFileSync, mkdirSync } from "node:fs";

const original = (name) => readFileSync(new URL(`../../prototypes/frontend/${name}.html`, import.meta.url), "utf8");
const words = (element) => {
  const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
  const pieces = [];
  while (walker.nextNode()) {
    if (!walker.currentNode.parentElement.closest("textarea, input, select, [aria-hidden='true']")) pieces.push(walker.currentNode.textContent);
  }
  return pieces.join(" ").replace(/\s+/g, " ").trim();
};

test.beforeEach(async ({ page }) => {
  await mockDesignApi(page);
  await page.addInitScript(() => sessionStorage.setItem("human-worth-preview-data", "on"));
});

async function matchesOriginal(page, name, pairs) {
  const source = original(name);
  for (const [sourceSelector, currentSelector = sourceSelector] of pairs) {
    const expected = await page.evaluate(({ source, selector }) => {
      const document = new DOMParser().parseFromString(source, "text/html");
      const element = document.querySelector(selector);
      if (!element) throw Error(`Missing source selector: ${selector}`);
      const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
      const pieces = [];
      while (walker.nextNode()) {
        if (!walker.currentNode.parentElement.closest("textarea, input, select, [aria-hidden='true']")) pieces.push(walker.currentNode.textContent);
      }
      return pieces.join(" ").replace(/\s+/g, " ").trim();
    }, { source, selector: sourceSelector });
    await expect.poll(() => page.locator(currentSelector).evaluate(words), { message: `${name}: ${sourceSelector}` }).toBe(expected);
  }
}

async function noTranslatedUI(page) {
  const copy = await page.evaluate(() => document.body.innerText + Array.from(document.querySelectorAll("[aria-label], [placeholder], [title]")).map((el) => [el.getAttribute("aria-label"), el.getAttribute("placeholder"), el.getAttribute("title")].join(" ")).join(" "));
  expect(copy).not.toMatch(/\p{Script=Han}/u);
  expect(await page.locator("html").getAttribute("lang")).toBe("en");
  expect(await page.title()).not.toMatch(/\p{Script=Han}/u);
}

test("four pages retain design headings and editor copy while API data supplies content", async ({ page }) => {
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  mkdirSync("artifacts/current-preview", { recursive: true });
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 1000 });
    for (const [name, route, pairs] of [
      ["index", "/", [["#opening-title"], [".opening .intro"], [".transition h2"], [".invitation h2"]]],
      ["discovery", "/discovery", [["h1"], [".description"], ["label[for='task-search']"]]],
      ["task-detail", "/tasks/concept-library", [["h1"], [".perspectives-heading h2"], [".statistics > p"], ["#discussion .lead"], ["label[for='comment-body']", "#discussion form > label"]]],
      ["submit", "/submit?edit=fixture-submission", [["h1"], [".hero-bottom > p"], [".title-field"], [".paired-fields label:first-child"], [".paired-fields label:last-child"], [".category-field"], [".shared-input > p"], [".review-note"], ["#save", ".submit-actions .secondary"], ["#send", ".submit-actions button:last-child"]]],
    ]) {
      await page.goto(route);
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      await matchesOriginal(page, name, pairs);
      await noTranslatedUI(page);
      await expect(page).toHaveTitle("Human Worth");
      await page.evaluate(() => document.fonts.ready);
      await page.screenshot({ path: `artifacts/current-preview/copy-${name}-${width}.png`, fullPage: true });
    }
    await page.getByRole("button", { name: "+ Add agent work", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Add a work.", exact: true })).toBeVisible();
    await matchesOriginal(page, "submit", [
      ["#work-dialog-title", "dialog[open] h2"],
      ["#work-dialog label:has(input[name='title'])", "dialog[open] label:has(input[maxlength='300'])"],
      ["#work-dialog label:has(textarea[name='body'])", "dialog[open] label:has(textarea[rows='7'])"],
      ["#work-dialog label:has(input[name='source'])", "dialog[open] label:has(input[placeholder='Who made this, and where did it come from?'])"],
      ["#work-dialog label:has(input[name='url'])", "dialog[open] label:has(input[type='url'])"],
      ["#work-dialog label:has(input[name='files'])", "dialog[open] label:has(input[type='file'])"],
      ["#work-dialog summary", "dialog[open] summary"],
      ["#work-dialog .permissions", "dialog[open] .permissions"],
      ["#remove-work", "dialog[open] .work-editor > button:last-child"],
      ["#work-form .submit-actions .button", "dialog[open] > .button"],
    ]);
    await noTranslatedUI(page);
    await page.screenshot({ path: `artifacts/current-preview/copy-work-dialog-${width}.png` });
    await page.getByRole("button", { name: "Close work editor", exact: true }).click();
    await page.getByRole("button", { name: "Preview the submission ↗", exact: true }).click();
    await expect(page.locator(".submission-preview .eyebrow").first()).toHaveText("Submission preview");
    await expect(page.locator(".submission-preview")).toContainText("A small pause, a warm welcome back.");
    await noTranslatedUI(page);
    await page.screenshot({ path: `artifacts/current-preview/copy-submission-preview-${width}.png` });
    await page.getByRole("button", { name: "Close preview", exact: true }).click();
    await page.goto("/tasks/concept-library");
    await page.locator(".chapter-cover").first().click();
    await expect(page.getByRole("button", { name: "Choose this work", exact: true })).toBeVisible();
    await matchesOriginal(page, "task-detail", [["#reader-choose", ".work-actions > button:first-child"], [".reader-keyboard"]]);
    await noTranslatedUI(page);
    await page.getByRole("button", { name: "View statistics…" }).click();
    await expect(page.getByRole("button", { name: "Close voting & view statistics" })).toBeVisible();
    await noTranslatedUI(page);
  }
  expect(errors).toEqual([]);
});
