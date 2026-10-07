const { test, expect } = require("@playwright/test");
const { login, card, agent } = require("./helpers");

test.beforeEach(() => agent.heartbeat());

test("tiles and cards show every gate state, red first", async ({ page }) => {
  await login(page);
  await expect(card(page, "STI-G01-POS01")).toBeVisible();

  // Tiles agree with the cards (other tests add events, so no fixed counts).
  const cards = page.locator("a.card");
  await expect(page.locator(".tile .value").nth(0)).toHaveText(String(await cards.count()));
  for (const [i, color] of [[1, "red"], [2, "yellow"], [3, "green"]]) {
    await expect(page.locator(".tile .value").nth(i)).toHaveText(String(await page.locator("a.card." + color).count()));
  }

  await expect(card(page, "STI-G04-POS01").locator(".status")).toHaveText("Mati");
  await expect(card(page, "STI-G05-POS01").locator(".status")).toHaveText("Terlambat");
  await expect(card(page, "LBI-G02-POS01").locator(".status")).toHaveText("Belum pernah lapor");
  await expect(card(page, "LBI-G02-POS01").locator(".hint")).toBeVisible();
  await expect(card(page, "STI-G03-POS01")).toContainText("Antrean 37");

  // Within each location, red cards come before yellow and green ones.
  for (const loc of ["STI", "LBI"]) {
    const colors = await page.locator(`section[aria-label="Lokasi ${loc}"] a.card`).evaluateAll(
      (els) => els.map((e) => ({ red: 0, yellow: 1, green: 2 })[e.className.replace("card ", "")]));
    expect(colors).toEqual([...colors].sort());
  }
});

test("refresh updates cards in place: focus, typed search and scroll survive", async ({ page }) => {
  await page.clock.install();
  await login(page);
  await expect(card(page, "STI-G01-POS01")).toBeVisible();

  // Tag a card's DOM node; if refresh rebuilt the page the tag would vanish.
  await card(page, "STI-G06-POS01").evaluate((el) => { el.dataset.e2e = "same-node"; });

  const search = page.getByLabel("Cari gate");
  await search.fill("sti");
  await page.clock.runFor(200); // let the search debounce apply under the fake clock
  await expect(card(page, "LBI-G01-POS01")).toBeHidden();
  await page.setViewportSize({ width: 1280, height: 500 });
  await page.mouse.wheel(0, 300);
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
  const scrollBefore = await page.evaluate(() => window.scrollY);
  await search.focus();

  // A refresh where nothing moved: everything stays exactly put.
  await page.clock.fastForward("00:16");
  await expect(page.locator(".updated")).toContainText("Diperbarui");
  expect(await page.evaluate(() => window.scrollY)).toBe(scrollBefore);
  await expect(card(page, "STI-G06-POS01")).toHaveAttribute("data-e2e", "same-node");
  await expect(search).toBeFocused();
  await expect(search).toHaveValue("sti");

  // A gate turns red: its card updates (and moves to the front) on the next poll.
  await agent.postEvent("STI-G01-POS01", "READER_NOT_READY", { message: "e2e: reader mati" });
  await page.clock.fastForward("00:16");
  await expect(card(page, "STI-G01-POS01")).toHaveClass(/red/);
  await expect(card(page, "STI-G06-POS01")).toHaveAttribute("data-e2e", "same-node");
  await expect(search).toBeFocused();
});

test("location filter and search hide cards without reloading", async ({ page }) => {
  await login(page);
  await page.getByLabel("Filter lokasi").selectOption("LBI");
  await expect(page.locator('section[aria-label="Lokasi STI"]')).toBeHidden();
  await expect(card(page, "LBI-G01-POS01")).toBeVisible();

  await page.getByLabel("Filter lokasi").selectOption("");
  await page.getByLabel("Cari gate").fill("g04");
  await expect(card(page, "STI-G04-POS01")).toBeVisible();
  await expect(card(page, "STI-G01-POS01")).toBeHidden();

  await page.getByLabel("Cari gate").fill("tidak-ada-gate-ini");
  await expect(page.locator(".empty")).toHaveText("Tidak ada gate yang cocok dengan filter.");
});

test("keyboard: skip link and cards are reachable and open the gate", async ({ page }) => {
  await login(page);
  // After login, focus lands on the page heading.
  await expect(page.getByRole("heading", { name: "Ringkasan gate" })).toBeFocused();

  // On a fresh load, the first Tab reaches the skip link.
  await page.reload();
  await expect(card(page, "STI-G02-POS01")).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(page.locator("#skip")).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Ringkasan gate" })).toBeFocused();

  await card(page, "STI-G02-POS01").focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "STI · G02" })).toBeFocused();
});
