// Accessibility (axe-core) on every page, phone layout checks, and
// screenshots of each page in light/dark for human review.
const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const { login, agent } = require("./helpers");

test.beforeEach(() => agent.heartbeat());

const PAGES = [
  ["ringkasan", "#/", "Ringkasan gate"],
  ["detail", "#/device/STI-G02-POS01", "STI · G02"],
  ["event", "#/events", "Event"],
];

async function axe(page) {
  const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
  return res.violations
    .filter((v) => v.impact === "serious" || v.impact === "critical")
    .map((v) => `${v.id} (${v.impact}): ${v.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(", ")}`);
}

test("login page has no serious accessibility violations @mobile", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Masuk" })).toBeVisible();
  expect(await axe(page)).toEqual([]);
});

for (const [name, hash, heading] of PAGES) {
  for (const scheme of ["light", "dark"]) {
    test(`${name} (${scheme}): no serious a11y violations, no sideways scroll @mobile`, async ({ page }, info) => {
      await page.emulateMedia({ colorScheme: scheme });
      await login(page, hash);
      await expect(page.getByRole("heading", { name: heading })).toBeVisible();
      await expect(page.locator(".skeleton-text")).toHaveCount(0, { timeout: 15000 });
      await page.waitForLoadState("networkidle");

      expect(await axe(page)).toEqual([]);
      // Only tables may scroll sideways (inside .table-wrap), never the page.
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      expect(overflow).toBeLessThanOrEqual(0);

      await info.attach(`${name}-${scheme}-${info.project.name}.png`, {
        body: await page.screenshot({ fullPage: true }), contentType: "image/png",
      });
    });
  }
}
