const { test, expect } = require("@playwright/test");
const { login, agent } = require("./helpers");

test.beforeEach(() => agent.heartbeat());

const rows = (page) => page.locator("table.events tbody tr.clickable");

test("filters go into the URL and are restored from it", async ({ page }) => {
  await login(page, "#/events");
  await page.getByLabel("Severity minimum").selectOption("HIGH");
  await page.getByLabel("Lokasi").fill("STI");
  await expect(page).toHaveURL(/#\/events\?.*severity=HIGH/);
  await expect(page).toHaveURL(/location=STI/);

  await page.reload();
  await expect(page.getByLabel("Severity minimum")).toHaveValue("HIGH");
  await expect(page.getByLabel("Lokasi")).toHaveValue("STI");
  await expect(rows(page).first()).toBeVisible();
  for (const loc of await rows(page).locator("td:nth-child(3)").allTextContents()) expect(loc).toMatch(/^STI /);
});

test("Back from a gate returns to the same filtered list", async ({ page }) => {
  await login(page, "#/events?severity=HIGH&range=7d");
  await expect(rows(page).first()).toBeVisible();
  await rows(page).first().locator("a").click();
  await expect(page.locator(".title-row h1")).toBeVisible();
  await page.goBack();
  await expect(page.getByLabel("Severity minimum")).toHaveValue("HIGH");
  await expect(page.getByLabel("Rentang waktu")).toHaveValue("7d");
});

test("a slow response for an old filter never overwrites the newer one", async ({ page }) => {
  await login(page, "#/events");
  await expect(rows(page).first()).toBeVisible();
  // Hold back the LOW-severity request; the HIGH one answers immediately.
  await page.route(/\/v1\/events\?.*severity=LOW/, async (route) => {
    await new Promise((r) => setTimeout(r, 1500));
    await route.continue().catch(() => {}); // aborted by the client: fine
  });
  await page.getByLabel("Severity minimum").selectOption("LOW");
  await page.getByLabel("Severity minimum").selectOption("HIGH");
  await page.waitForTimeout(2000);
  const sev = await rows(page).locator(".sev").allTextContents();
  expect(sev.length).toBeGreaterThan(0);
  expect(sev.every((s) => s === "HIGH" || s === "CRITICAL")).toBe(true);
});

test("event detail opens with the keyboard", async ({ page }) => {
  await login(page, "#/events");
  const toggle = rows(page).first().getByRole("button", { name: /Detail event/ });
  await toggle.focus();
  await page.keyboard.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  const detail = page.locator("tr.detail").first();
  await expect(detail).toBeVisible();
  await expect(detail).toContainText("event_id");
  await page.keyboard.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
});

test("untrusted event text is shown as text, never as HTML", async ({ page }) => {
  await agent.postEvent("STI-G06-POS01", "PRINT_FAILED", { message: '<img src=x onerror="window.__xss=1">' });
  await login(page, "#/events?device_id=STI-G06-POS01");
  await expect(rows(page).first()).toContainText('<img src=x onerror="window.__xss=1">');
  expect(await page.evaluate(() => window.__xss)).toBeUndefined();
});
