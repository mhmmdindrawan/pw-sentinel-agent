// @ts-check
const { defineConfig, devices } = require("@playwright/test");

// One agent process is shared by all tests and some tests stop/start it,
// so tests run serially in a single worker.
module.exports = defineConfig({
  testDir: "./tests",
  workers: 1,
  fullyParallel: false,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  retries: 0,
  reporter: [["list"], ["html", { open: "never" }]],
  globalSetup: require.resolve("./global-setup.js"),
  use: {
    baseURL: "http://127.0.0.1:18797",
    channel: "chrome", // system Chrome; no browser download
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    locale: "id-ID",
    timezoneId: "Asia/Jakarta",
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"], channel: "chrome", viewport: { width: 1280, height: 900 } } },
    { name: "mobile", use: { ...devices["Pixel 7"], channel: "chrome" }, grep: /@mobile/ },
  ],
});
