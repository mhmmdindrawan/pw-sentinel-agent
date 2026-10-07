const { test, expect } = require("@playwright/test");
const { login, agent } = require("./helpers");

test.beforeEach(() => agent.heartbeat());

test("gate detail shows status, tiles, chart and summary", async ({ page }) => {
  await login(page, "#/device/STI-G02-POS01");
  await expect(page.getByRole("heading", { name: "STI · G02" })).toBeVisible();
  await expect(page.locator(".title-row .status")).toHaveText("Bermasalah");
  await expect(page.locator(".tile .value").first()).not.toHaveText("00");
  await expect(page.locator(".chart svg path.bar").first()).toBeVisible();
  await expect(page.getByRole("img", { name: /Error per jam/ })).toBeVisible();
  await expect(page.locator("table").filter({ hasText: "READER_NOT_READY" }).first()).toBeVisible();
  await expect(page).toHaveTitle("STI G02 — Parkways Sentinel");
});

test("new events show a notice instead of reshuffling open rows", async ({ page }) => {
  await page.clock.install();
  await login(page, "#/device/STI-G06-POS01");
  const firstToggle = page.locator("table.events tbody tr.clickable").first().getByRole("button");
  await firstToggle.click();
  await expect(firstToggle).toHaveAttribute("aria-expanded", "true");

  await agent.postEvent("STI-G06-POS01", "MQTT_DISCONNECTED", { message: "e2e: event baru" });
  await page.clock.fastForward("00:31");
  await expect(page.getByText("Ada event baru.")).toBeVisible();
  await expect(firstToggle).toHaveAttribute("aria-expanded", "true"); // still open

  await page.getByRole("button", { name: "Tampilkan" }).click();
  await expect(page.locator("table.events tbody tr.clickable").first()).toContainText("MQTT_DISCONNECTED");
});

test("unknown gate shows a friendly not-found page", async ({ page }) => {
  await login(page, "#/device/TIDAK-ADA");
  await expect(page.getByRole("heading", { name: "Gate tidak ditemukan" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Kembali ke ringkasan" })).toBeVisible();
  await expect(page.locator("#conn")).toBeHidden(); // a 404 is not a connection problem
});
