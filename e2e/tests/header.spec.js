const { test, expect } = require("@playwright/test");
const { login } = require("./helpers");

test("header nav shows its icons and marks the current page", async ({ page }) => {
  // The nav icons are data: SVGs in app.css; a CSP that blocks them leaves
  // empty space next to the labels.
  const csp = [];
  page.on("console", (m) => { if (/Content Security Policy/i.test(m.text())) csp.push(m.text()); });
  await login(page);

  const topbar = page.locator("#topbar");
  const box = await topbar.boundingBox();
  expect(box.y).toBe(0);
  expect(box.width).toBe(page.viewportSize().width);

  await expect(page.getByRole("link", { name: "Ringkasan" })).toHaveAttribute("aria-current", "page");
  await page.getByRole("link", { name: "Event" }).click();
  await expect(page).toHaveURL(/#\/events$/);
  await expect(page.getByRole("link", { name: "Event" })).toHaveAttribute("aria-current", "page");
  expect(csp).toEqual([]);
});
