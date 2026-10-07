const { expect } = require("@playwright/test");
const agent = require("../agent");

async function login(page, hash = "#/") {
  await page.goto("/" + hash);
  await page.getByLabel("Username").fill(agent.USER);
  await page.getByLabel("Password").fill(agent.PASSWORD);
  await page.getByRole("button", { name: "Masuk" }).click();
  await expect(page.locator("#topbar")).toBeVisible();
}

// card returns the fleet card for a device.
function card(page, deviceId) {
  return page.locator(`a.card[href="#/device/${deviceId}"]`);
}

module.exports = { login, card, agent };
