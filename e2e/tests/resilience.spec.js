const { test, expect } = require("@playwright/test");
const { login, card, agent } = require("./helpers");

test.beforeEach(() => agent.heartbeat());
test.afterEach(() => agent.start()); // never leave the agent down for the next test

test("server going away keeps the data on screen and recovers by itself", async ({ page }) => {
  await page.clock.install();
  await login(page);
  await expect(card(page, "STI-G02-POS01")).toBeVisible();
  const banner = page.locator("#conn");
  await expect(banner).toBeHidden();

  await agent.stop();
  await page.clock.fastForward("00:16");
  await expect(banner).toBeVisible();
  await expect(banner).toContainText("Koneksi ke server Sentinel terputus");
  await expect(banner).toContainText("data terakhir");
  // The dashboard is still fully there.
  await expect(card(page, "STI-G02-POS01")).toBeVisible();
  await expect(page.locator(".tile .value").first()).toHaveText("8");

  // After two missed intervals the header says the data is old.
  await page.clock.fastForward("00:31");
  await expect(page.locator(".updated")).toHaveClass(/stale/);

  await agent.start();
  await page.getByRole("button", { name: "Coba sekarang" }).click();
  await expect(banner).toBeHidden();
  await expect(page.locator(".updated")).not.toHaveClass(/stale/);
});

test("event list keeps rows and offers retry when a page fails to load", async ({ page }) => {
  await login(page, "#/events");
  await expect(page.locator("table.events tbody tr.clickable").first()).toBeVisible();
  const rows = await page.locator("table.events tbody tr.clickable").count();

  await agent.stop();
  await page.getByLabel("Severity minimum").selectOption("HIGH");
  await expect(page.getByText(/Gagal memuat event/)).toBeVisible();
  await expect(page.locator("table.events tbody tr.clickable")).toHaveCount(rows);

  await agent.start();
  await page.getByRole("button", { name: "Coba lagi" }).click();
  await expect(page.getByText(/Gagal memuat event/)).toBeHidden();
  const sev = await page.locator("table.events tbody tr.clickable .sev").allTextContents();
  expect(sev.length).toBeGreaterThan(0);
  expect(sev.every((s) => s === "HIGH" || s === "CRITICAL")).toBe(true);
});

test("API unreachable on first load shows a retry screen, not a blank page", async ({ page }) => {
  await login(page);
  // The page itself loads, but the API can't be reached (e.g. the agent
  // crashed right after serving it).
  await page.route("**/v1/auth/me", (route) => route.abort("connectionrefused"));
  await page.reload();
  await expect(page.getByRole("heading", { name: "Server tidak dapat dihubungi" })).toBeVisible();
  await page.unroute("**/v1/auth/me");
  await page.getByRole("button", { name: "Coba lagi" }).click();
  await expect(page.getByRole("heading", { name: "Ringkasan gate" })).toBeVisible();
});
