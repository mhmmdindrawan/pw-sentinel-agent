// Controls the agent under test: a fresh demo installation (seeded by
// cmd/sentinel-demo) in e2e/.run, started and stopped on demand so tests
// can simulate the server going away.
const { spawn, execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");

const ROOT = path.resolve(__dirname, "..");
const RUN = path.join(__dirname, ".run");
const BIN = path.join(RUN, "sentinel-agent");
const DEMO = path.join(RUN, "demo");
const PORT = 18797;
const BASE = `http://127.0.0.1:${PORT}`;
const PID = path.join(RUN, "agent.pid");

const LIVE = [
  ["STI-G01-POS01", 0], ["STI-G02-POS01", 0], ["STI-G03-POS01", 37], ["STI-G06-POS01", 0], ["LBI-G01-POS01", 0],
];

function build() {
  fs.rmSync(RUN, { recursive: true, force: true });
  fs.mkdirSync(RUN, { recursive: true });
  const agentDir = path.join(ROOT, "agent");
  execFileSync("go", ["build", "-o", BIN, "./cmd/sentinel-agent"], { cwd: agentDir, stdio: "inherit" });
  execFileSync("go", ["run", "./cmd/sentinel-demo", "-dir", DEMO, "-registry", "registry.yaml", "-listen", `127.0.0.1:${PORT}`],
    { cwd: agentDir, stdio: "inherit" });
}

async function start() {
  await stop();
  const log = fs.openSync(path.join(RUN, "agent.log"), "a");
  const child = spawn(BIN, ["-config", "config.yaml"], { cwd: DEMO, stdio: ["ignore", log, log], detached: true });
  child.unref();
  fs.writeFileSync(PID, String(child.pid));
  for (let i = 0; i < 100; i++) {
    try {
      const r = await fetch(BASE + "/v1/health");
      if (r.ok) break;
    } catch (_) { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 100));
  }
  await heartbeat();
}

async function stop() {
  if (!fs.existsSync(PID)) return;
  const pid = Number(fs.readFileSync(PID, "utf8"));
  fs.rmSync(PID);
  try { process.kill(pid, "SIGTERM"); } catch (_) { return; }
  for (let i = 0; i < 50; i++) {
    try { process.kill(pid, 0); } catch (_) { return; }
    await new Promise((r) => setTimeout(r, 100));
  }
}

// heartbeat keeps the live demo gates ONLINE for the duration of a test.
async function heartbeat() {
  await Promise.all(LIVE.map(([id, outbox]) => fetch(BASE + "/v1/heartbeat", {
    method: "POST",
    headers: { Authorization: "Bearer demo-token-" + id, "Content-Type": "application/json" },
    body: JSON.stringify({ device_id: id, app_version: "3.1.4", sdk_version: "0.2.0", uptime_s: 32520, outbox_pending: outbox }),
  })));
}

async function postEvent(deviceId, event, extra = {}) {
  const r = await fetch(BASE + "/v1/events", {
    method: "POST",
    headers: { Authorization: "Bearer demo-token-" + deviceId, "Content-Type": "application/json" },
    body: JSON.stringify({ device_id: deviceId, event, ...extra }),
  });
  return r.json();
}

module.exports = { build, start, stop, heartbeat, postEvent, BASE, USER: "demo", PASSWORD: "demo-password-1" };
