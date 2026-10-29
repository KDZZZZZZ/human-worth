import { test, expect } from "@playwright/test";

const session = {
  account: { id: "u1", displayName: "Reader" },
  role: "user",
  csrfToken: "csrf-cached",
};

test("cached session skips Checking session during a full page navigation", async ({
  page,
}) => {
  let meDelay = 0;
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/me") {
      if (meDelay) await new Promise((resolve) => setTimeout(resolve, meDelay));
      return route.fulfill({ json: session });
    }
    return route.fulfill({ json: { items: [], nextCursor: null } });
  });

  await page.goto("/");
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
  expect(
    await page.evaluate(() =>
      sessionStorage.getItem("human-worth:session"),
    ),
  ).toContain("csrf-cached");

  meDelay = 1200;
  await page.click('a[href="/discovery"]');
  await page.waitForURL("**/discovery");

  const firstAccountText = await page.evaluate(
    () =>
      new Promise((resolve) => {
        const read = () =>
          document.querySelector(".global-account")?.textContent ?? null;
        const seen = read();
        if (seen) return resolve(seen);
        const observer = new MutationObserver(() => {
          const text = read();
          if (text) {
            observer.disconnect();
            resolve(text);
          }
        });
        observer.observe(document.body, {
          childList: true,
          subtree: true,
          characterData: true,
        });
        setTimeout(() => {
          observer.disconnect();
          resolve(null);
        }, 5000);
      }),
  );
  expect(firstAccountText).toBe("Sign out");
});

test("header shows Sign in while the session check is pending", async ({
  page,
}) => {
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/me") {
      await new Promise((resolve) => setTimeout(resolve, 1500));
      return route.fulfill({ json: session });
    }
    return route.fulfill({ json: { items: [], nextCursor: null } });
  });

  await page.goto("/", { waitUntil: "domcontentloaded" });
  await expect(page.getByRole("link", { name: "Sign in" })).toBeVisible();
  await expect(page.getByText(/Checking session/)).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
});

test("an expired session clears the cached account", async ({ page }) => {
  let authenticated = true;
  await page.route("**/api/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/me") {
      return authenticated
        ? route.fulfill({ json: session })
        : route.fulfill({
            status: 401,
            json: {
              code: "unauthenticated",
              detail: "Unauthorized",
              status: 401,
              title: "Unauthorized",
              type: "about:blank",
            },
          });
    }
    return route.fulfill({ json: { items: [], nextCursor: null } });
  });

  await page.goto("/");
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();

  authenticated = false;
  await page.click('a[href="/discovery"]');
  await expect(page.getByRole("link", { name: "Sign in" })).toBeVisible();
  expect(
    await page.evaluate(() =>
      sessionStorage.getItem("human-worth:session"),
    ),
  ).toBeNull();
});
