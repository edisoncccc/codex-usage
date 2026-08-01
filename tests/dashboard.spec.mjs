import { test, expect } from "@playwright/test";
import { mkdir, mkdtemp, writeFile, rm } from "node:fs/promises";
import { once } from "node:events";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";

let processHandle;
let stateDir;
let codexHomeDir;
let baseURL;

test.beforeAll(async () => {
  const binary = process.env.CODEX_USAGE_BIN;
  if (!binary) throw new Error("CODEX_USAGE_BIN must point to a built codex-usage binary");
  stateDir = await mkdtemp(path.join(tmpdir(), "codex-usage-e2e-"));
  const port = 45000 + (process.pid % 1000);
  codexHomeDir = await mkdtemp(path.join(tmpdir(), "codex-usage-codex-home-"));
  const codexHome = codexHomeDir;
  const now = new Date();
  const sessionDir = path.join(codexHome, "sessions", String(now.getFullYear()), String(now.getMonth() + 1).padStart(2, "0"), String(now.getDate()).padStart(2, "0"));
  await mkdir(sessionDir, { recursive: true });
  const timestamp = now.toISOString();
  const fixture = [
    { timestamp, type: "session_meta", payload: { id: "e2e-session", cwd: "C:\\work\\codex-usage-e2e", originator: "codex_desktop" } },
    { timestamp, type: "turn_context", payload: { turn_id: "turn-1", cwd: "C:\\work\\codex-usage-e2e", model: "gpt-5.4" } },
    { timestamp, type: "event_msg", payload: { type: "token_count", info: {
      total_token_usage: { input_tokens: 80, cached_input_tokens: 20, cache_write_input_tokens: 0, output_tokens: 20, reasoning_output_tokens: 5, total_tokens: 100 },
      last_token_usage: { input_tokens: 80, cached_input_tokens: 20, cache_write_input_tokens: 0, output_tokens: 20, reasoning_output_tokens: 5, total_tokens: 100 }
    } } }
  ];
  await writeFile(path.join(sessionDir, "rollout-e2e.jsonl"), `${fixture.map((item) => JSON.stringify(item)).join("\n")}\n`);
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
      CODEX_HOME: codexHome
    },
    stdio: "ignore",
    windowsHide: true
  });
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseURL}/healthz`);
      if (response.ok) {
        const summary = await fetch(`${baseURL}/api/v1/summary?since=today`);
        if (summary.ok && (await summary.json()).grand_total >= 100) return;
      }
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
      delay(5_000).then(() => { throw new Error("codex-usage test server did not exit in time"); })
    ]);
  }
  if (stateDir) await rm(stateDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
  if (codexHomeDir) await rm(codexHomeDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
});

test("overview is calm, local-only, and exposes honest cost coverage", async ({ page }) => {
  const consoleErrors = [];
  const externalRequests = [];
  page.on("console", (message) => { if (message.type() === "error") consoleErrors.push(message.text()); });
  page.on("pageerror", (error) => consoleErrors.push(error.message));
  page.on("request", (request) => {
    if (new URL(request.url()).origin !== baseURL) externalRequests.push(request.url());
  });
  await page.goto(baseURL, { waitUntil: "networkidle" });

  await expect(page.getByRole("heading", { name: "本机用量概览" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "概览" })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("过去 7 个本地自然日")).toBeVisible();
  await expect(page.getByRole("heading", { name: "每日脉冲带" })).toBeVisible();
  await expect(page.locator("#view-overview").getByText("Standard API 等价成本", { exact: true })).toBeVisible();
  await expect(page.locator("#machineId")).not.toHaveText("—");
  await expect(page.locator("#overviewCoverage")).toContainText("已定价 100.0% Token");
  const styleIntegrity = await page.evaluate(() => ({
    bodyFontSize: getComputedStyle(document.body).fontSize,
    skipTop: getComputedStyle(document.querySelector(".skip-link")).top,
    topbarPosition: getComputedStyle(document.querySelector(".topbar")).position,
    heroDisplay: getComputedStyle(document.querySelector(".hero-metrics")).display
  }));
  expect(styleIntegrity).toEqual({ bodyFontSize: "14px", skipTop: "-60px", topbarPosition: "sticky", heroDisplay: "grid" });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(1440);
  expect(externalRequests).toEqual([]);
  expect(consoleErrors).toEqual([]);
});

test("navigation, month drill-down, filter chips, pricing, and scan feedback work", async ({ page }) => {
  await page.goto(baseURL, { waitUntil: "networkidle" });

  const dailyTab = page.getByRole("tab", { name: "每日" });
  await dailyTab.focus();
  await dailyTab.press("Enter");
  await expect(page.getByRole("heading", { name: "每日用量" })).toBeVisible();
  const initialMonth = await page.locator("#monthLabel").textContent();
  await page.locator("#previousMonth").click();
  await expect(page.locator("#monthLabel")).not.toHaveText(initialMonth);
  expect(await page.locator("[data-calendar-date]").count()).toBeGreaterThan(0);

  await page.getByRole("tab", { name: "明细" }).click();
  await expect(page.getByRole("heading", { name: "明细与归属" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Session 明细" })).toBeVisible();
  await page.getByRole("tab", { name: "项目" }).click();
  await expect(page.getByRole("heading", { name: "按项目" })).toBeVisible();

  await page.locator("#filterButton").click();
  await expect(page.locator("#filterSheet")).toBeVisible();
  await page.locator("#filterAgent").selectOption("main");
  await page.locator("#applyFilters").click();
  await expect(page.locator(".filter-chip")).toContainText("Agent · main");
  await page.locator("[data-remove-filter=agent_type]").click();
  await expect(page.locator(".filter-chip")).toHaveCount(0);

  await page.getByRole("tab", { name: "概览" }).click();
  await page.locator("#pricingButton").click();
  await page.locator("#newOverrideModel").fill("e2e-internal-model");
  await page.locator("#addOverride").click();
  const card = page.locator('[data-pricing-model="e2e-internal-model"]');
  await card.locator("[data-rate-mode]").selectOption("custom");
  await card.locator('[data-rate="input_usd_per_million"]').fill("1.00");
  await card.locator('[data-rate="cached_input_usd_per_million"]').fill("0.10");
  await card.locator('[data-rate="cache_write_input_usd_per_million"]').fill("1.25");
  await card.locator('[data-rate="output_usd_per_million"]').fill("6.00");
  await page.locator("#savePricing").click();
  await expect(page.getByText("本机定价已保存，费用已重新估算")).toBeVisible();
  await expect.poll(async () => {
    const response = await page.request.get(`${baseURL}/api/v1/pricing`);
    return (await response.json()).overrides["e2e-internal-model"]?.output_usd_per_million;
  }).toBe("6.00");

  await page.locator("#scanButton").click();
  await expect(page.getByText(/扫描完成：新增/)).toBeVisible();
});

test("revisiting a range or view reuses the current data revision", async ({ page }) => {
  const dataRequests = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (["/api/v1/cost-estimate", "/api/v1/breakdown", "/api/v1/sessions"].includes(url.pathname)) {
      dataRequests.push(`${url.pathname}?${url.searchParams.toString()}`);
    }
  });
  await page.goto(baseURL, { waitUntil: "networkidle" });

  await page.locator('[data-overview-range="all"]').click();
  await expect(page.locator("#overviewCost")).not.toHaveClass(/loading/);
  const allCostKey = "/api/v1/cost-estimate?bucket=day";
  const firstAllCount = dataRequests.filter((item) => item === allCostKey).length;
  expect(firstAllCount).toBe(1);

  await page.locator('[data-overview-range="30d"]').click();
  await expect(page.locator("#overviewCost")).not.toHaveClass(/loading/);
  await page.locator('[data-overview-range="all"]').click();
  await expect(page.locator("#overviewCost")).not.toHaveClass(/loading/);
  await page.waitForTimeout(100);
  expect(dataRequests.filter((item) => item === allCostKey).length).toBe(firstAllCount);

  await page.getByRole("tab", { name: "每日" }).click();
  await expect(page.getByRole("heading", { name: "每日用量" })).toBeVisible();
  const monthCostKey = dataRequests.find((item) => item.startsWith("/api/v1/cost-estimate?since=") && item.includes("&bucket=day"));
  expect(monthCostKey).toBeTruthy();
  const firstMonthCount = dataRequests.filter((item) => item === monthCostKey).length;
  await page.getByRole("tab", { name: "明细" }).click();
  await expect(page.getByRole("heading", { name: "明细与归属" })).toBeVisible();
  await page.getByRole("tab", { name: "每日" }).click();
  await expect(page.getByRole("heading", { name: "每日用量" })).toBeVisible();
  await page.waitForTimeout(100);
  expect(dataRequests.filter((item) => item === monthCostKey).length).toBe(firstMonthCount);
});

test("mobile, tablet, themes, and reduced motion avoid page overflow", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(baseURL, { waitUntil: "networkidle" });
  await page.getByRole("tab", { name: "明细" }).click();
  await expect(page.getByRole("heading", { name: "Session 明细" })).toBeVisible();
  expect(await page.locator(".session-row").count()).toBeGreaterThan(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await expect(page.locator("#exportButton")).toBeVisible();

  await page.setViewportSize({ width: 1024, height: 900 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(1024);
  await page.getByRole("tab", { name: "概览" }).click();
  await page.locator("#themeButton").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", /light|dark/);

  await page.emulateMedia({ reducedMotion: "reduce" });
  const duration = await page.locator(".pressable").first().evaluate((node) => parseFloat(getComputedStyle(node).transitionDuration));
  expect(duration).toBeLessThanOrEqual(.001);
});

test("mutation endpoints block foreign origins", async ({ request }) => {
  for (const [method, endpoint, data] of [
    ["post", "/api/v1/rescan", {}],
    ["put", "/api/v1/pricing/overrides", { overrides: {} }]
  ]) {
    const response = await request[method](`${baseURL}${endpoint}`, {
      headers: { Origin: "https://example.invalid" },
      data
    });
    expect(response.status()).toBe(403);
  }
});
