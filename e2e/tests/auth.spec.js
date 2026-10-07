const { test, expect } = require("@playwright/test");
const { login, agent } = require("./helpers");

test.beforeEach(() => agent.heartbeat());

test("wrong password shows a clear message and keeps the form usable", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Username").fill("demo");
  await page.getByLabel("Password").fill("salah-password");
  await page.getByRole("button", { name: "Masuk" }).click();
  await expect(page.getByRole("alert")).toHaveText("Username atau password salah.");
  await expect(page.getByLabel("Password")).toBeFocused();
  await expect(page.getByLabel("Password")).toHaveValue("");
});

test("login, then logout returns to the login form", async ({ page }) => {
  await login(page);
  await expect(page.getByRole("heading", { name: "Ringkasan gate" })).toBeVisible();
  await page.getByRole("button", { name: "Keluar" }).click();
  await expect(page.getByRole("button", { name: "Masuk" })).toBeVisible();
  await expect(page.getByRole("alert")).toHaveText("Anda sudah keluar.");
});

test("expired session sends you to login and back to the same page", async ({ page, context }) => {
  await page.clock.install();
  await login(page, "#/device/STI-G02-POS01");
  await expect(page.getByRole("heading", { name: "STI · G02" })).toBeVisible();

  await context.clearCookies();
  await page.clock.fastForward("00:31"); // next device refresh gets a 401
  await expect(page.getByRole("alert")).toContainText("Sesi Anda berakhir");

  await page.getByLabel("Username").fill(agent.USER);
  await page.getByLabel("Password").fill(agent.PASSWORD);
  await page.getByRole("button", { name: "Masuk" }).click();
  await expect(page.getByRole("heading", { name: "STI · G02" })).toBeVisible();
  expect(page.url()).toContain("#/device/STI-G02-POS01");
});

test("deep link from Telegram lands on the gate after login", async ({ page }) => {
  await login(page, "#/device/STI-G04-POS01");
  await expect(page.getByRole("heading", { name: "STI · G04" })).toBeVisible();
  await expect(page.locator(".title-row .status")).toHaveText("Mati");
});
