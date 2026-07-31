import { test, expect } from "@playwright/test";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { once } from "node:events";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";

let processHandle;
let stateDir;
let baseURL;

test.beforeAll(async () => {
  const binary = process.env.CODEX_USAGE_BIN;
  if (!binary) throw new Error("CODEX_USAGE_BIN must point to a built codex-usage binary");
  stateDir = await mkdtemp(path.join(tmpdir(), "codex-usage-e2e-"));
  const port = 45000 + (process.pid % 1000);
  await writeFile(path.join(stateDir, "config.json"), JSON.stringify({
    listen_address: "127.0.0.1",
    port,
    scan_interval_seconds: 60
  }));
  baseURL = `http://127.0.0.1:${port}`;
  processHandle = spawn(path.resolve(binary), ["serve"], {
    env: {
      ...process.env,
      CODEX_USAGE_HOME: stateDir,
      CODEX_HOME: path.join(stateDir, "empty-codex-home")
    },
    stdio: "ignore",
    windowsHide: true
  });
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseURL}/healthz`);
      if (response.ok) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("codex-usage test server did not become ready");
});

test.afterAll(async () => {
  if (processHandle && processHandle.exitCode === null && processHandle.signalCode === null) {
    const exited = once(processHandle, "exit");
    processHandle.kill();
    await Promise.race([
      exited,
      delay(5_000).then(() => {
        throw new Error("codex-usage test server did not exit in time");
      })
    ]);
  }
  if (stateDir) {
    await rm(stateDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
  }
});

test("dashboard renders offline status, cards, filters and session table", async ({ page }) => {
  const consoleErrors = [];
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });
  await page.goto(baseURL, { waitUntil: "networkidle" });
  await expect(page.getByRole("heading", { name: "Codex Usage" })).toBeVisible();
  await expect(page.getByText("本机累计")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Token 趋势" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Codex 来源" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "按项目" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "按 Thread" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Session 明细" })).toBeVisible();
  await expect(page.locator("#machineId")).not.toHaveText("—");
  const screenshot = await page.screenshot({ fullPage: true, animations: "disabled" });
  expect(screenshot.byteLength).toBeGreaterThan(10_000);
  expect(consoleErrors).toEqual([]);
});

test("mutation endpoint blocks foreign origin", async ({ request }) => {
  const response = await request.post(`${baseURL}/api/v1/rescan`, {
    headers: { Origin: "https://example.invalid" },
    data: {}
  });
  expect(response.status()).toBe(403);
});
