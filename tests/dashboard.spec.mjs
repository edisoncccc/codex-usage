import { test, expect } from "@playwright/test";
import { mkdir, mkdtemp, writeFile, rm } from "node:fs/promises";
import { once } from "node:events";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

let processHandle;
let stateDir;
let codexHomeDir;
let testBinaryDir;
let baseURL;
let dashboardURL;
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

async function findFreePort() {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close((error) => error ? reject(error) : resolve(port));
    });
  });
}

async function buildTestBinary() {
  testBinaryDir = await mkdtemp(path.join(tmpdir(), "codex-usage-e2e-bin-"));
  const binary = path.join(testBinaryDir, process.platform === "win32" ? "codex-usage.exe" : "codex-usage");
  const build = spawn(process.env.GO_BINARY || "go", ["build", "-trimpath", "-o", binary, "./cmd/codex-usage"], {
    cwd: repoRoot,
    stdio: "inherit",
    windowsHide: true
  });
  await new Promise((resolve, reject) => {
    build.once("error", reject);
    build.once("exit", (code, signal) => {
      if (code === 0) resolve();
      else reject(new Error(`go build failed (code=${code}, signal=${signal || "none"})`));
    });
  });
  return binary;
}

test.beforeAll(async () => {
  const binary = process.env.CODEX_USAGE_BIN || await buildTestBinary();
  stateDir = await mkdtemp(path.join(tmpdir(), "codex-usage-e2e-"));
  const port = await findFreePort();
  codexHomeDir = await mkdtemp(path.join(tmpdir(), "codex-usage-codex-home-"));
  const codexHome = codexHomeDir;
  const now = new Date();
  const currentHour = new Date(now);
  currentHour.setMinutes(0, 0, 0);
  const previousHourTimestamp = new Date(currentHour.getTime() - 30 * 60_000).toISOString();
  const sessionDir = path.join(codexHome, "sessions", String(now.getFullYear()), String(now.getMonth() + 1).padStart(2, "0"), String(now.getDate()).padStart(2, "0"));
  await mkdir(sessionDir, { recursive: true });
  const timestamp = now.toISOString();
  const fixture = [
    { timestamp: previousHourTimestamp, type: "session_meta", payload: { id: "e2e-session", cwd: "C:\\work\\codex-usage-e2e", originator: "codex_desktop" } },
    { timestamp: previousHourTimestamp, type: "turn_context", payload: { turn_id: "turn-1", cwd: "C:\\work\\codex-usage-e2e", model: "gpt-5.4" } },
    { timestamp: previousHourTimestamp, type: "event_msg", payload: { type: "token_count", info: {
      total_token_usage: { input_tokens: 48, cached_input_tokens: 12, cache_write_input_tokens: 0, output_tokens: 12, reasoning_output_tokens: 3, total_tokens: 60 },
      last_token_usage: { input_tokens: 48, cached_input_tokens: 12, cache_write_input_tokens: 0, output_tokens: 12, reasoning_output_tokens: 3, total_tokens: 60 }
    } } },
    { timestamp, type: "event_msg", payload: { type: "token_count", info: {
      total_token_usage: { input_tokens: 80, cached_input_tokens: 20, cache_write_input_tokens: 0, output_tokens: 20, reasoning_output_tokens: 5, total_tokens: 100 },
      last_token_usage: { input_tokens: 32, cached_input_tokens: 8, cache_write_input_tokens: 0, output_tokens: 8, reasoning_output_tokens: 2, total_tokens: 40 }
    } } }
  ];
  await writeFile(path.join(sessionDir, "rollout-e2e.jsonl"), `${fixture.map((item) => JSON.stringify(item)).join("\n")}\n`);
  await writeFile(path.join(stateDir, "config.json"), JSON.stringify({
    listen_address: "127.0.0.1",
    port,
    scan_interval_seconds: 600
  }));
  baseURL = `http://127.0.0.1:${port}`;
  dashboardURL = `${baseURL}/?lang=zh-CN`;
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
        const summary = await fetch(`${baseURL}/api/v1/summary?since=7d`);
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
  if (testBinaryDir) await rm(testBinaryDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
});

test("overview is calm, local-only, and exposes honest cost coverage", async ({ page }) => {
  const consoleErrors = [];
  const externalRequests = [];
  await page.route("**/api/v1/cost-estimate?*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("bucket") !== "day") {
      await route.continue();
      return;
    }
    const response = await route.fetch();
    const payload = await response.json();
    await route.fulfill({
      response,
      json: {
        ...payload,
        summary: {
          ...payload.summary,
          usd: "4801.10",
          regular_input_usd: "1.11",
          cached_input_usd: "22.22",
          cache_write_input_usd: "333.33",
          output_usd: "4444.44"
        }
      }
    });
  });
  page.on("console", (message) => { if (message.type() === "error") consoleErrors.push(message.text()); });
  page.on("pageerror", (error) => consoleErrors.push(error.message));
  page.on("request", (request) => {
    if (new URL(request.url()).origin !== baseURL) externalRequests.push(request.url());
  });
  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  await expect(page.getByRole("heading", { name: "本机用量概览" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "概览" })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("过去 7 个本地自然日")).toBeVisible();
  const cachedRate = page.locator("#overviewTokenBreakdown span", { hasText: "Cached Rate" });
  await expect(cachedRate).toContainText("25.0%");
  const trend = page.locator("#usageTrendPanel");
  await expect(trend.getByRole("heading", { name: "每日Token用量" })).toBeVisible();
  await expect(trend.getByRole("tab", { name: "每日" })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("#trendHourlyPane")).toBeHidden();
  await expect(page.locator("#trendDailyPane")).toBeVisible();
  await trend.getByRole("tab", { name: "每小时" }).click();
  await expect(trend.getByRole("heading", { name: "每小时Token用量" })).toBeVisible();
  await expect(trend.getByRole("tab", { name: "每小时" })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("#view-overview").getByText("Standard API 等价成本", { exact: true })).toBeVisible();
  const costBreakdown = page.locator("#overviewCostBreakdown");
  for (const [label, amount] of [
    ["普通 Input", "$1.11"],
    ["Cached Input", "$22.22"],
    ["Cache Write", "$333.33"],
    ["Output", "$4,444.44"]
  ]) {
    await expect(costBreakdown.locator("span", { hasText: label })).toHaveText(`${label}${amount}`);
  }
  await expect(page.locator("#machineId")).not.toHaveText("—");
  await expect(page.locator("#overviewCoverage")).toContainText("已定价 100.0% Token");
  const styleIntegrity = await page.evaluate(() => ({
    bodyFontSize: getComputedStyle(document.body).fontSize,
    supportingFontSize: getComputedStyle(document.querySelector(".eyebrow")).fontSize,
    skipTop: getComputedStyle(document.querySelector(".skip-link")).top,
    topbarPosition: getComputedStyle(document.querySelector(".topbar")).position,
    heroDisplay: getComputedStyle(document.querySelector(".hero-metrics")).display
  }));
  expect(styleIntegrity).toEqual({ bodyFontSize: "15px", supportingFontSize: "11px", skipTop: "-60px", topbarPosition: "sticky", heroDisplay: "grid" });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(1440);
  expect(externalRequests).toEqual([]);
  expect(consoleErrors).toEqual([]);
});

test("cached rate is unavailable without input tokens", async ({ page }) => {
  await page.route("**/api/v1/summary?*", async (route) => {
    const response = await route.fetch();
    const summary = await response.json();
    await route.fulfill({
      response,
      json: { ...summary, grand_total: 0, usage: { ...summary.usage, input: 0, cached_input: 0 } }
    });
  });
  await page.goto(dashboardURL, { waitUntil: "networkidle" });
  const cachedRate = page.locator("#overviewTokenBreakdown span", { hasText: "Cached Rate" });
  await expect(cachedRate).toHaveText("Cached Rate—");
});

test("auto-handled counter resets stay quiet and reviewable", async ({ page }) => {
  const warnings = Array.from({ length: 4 }, (_, index) => ({
    id: index + 1,
    created_at: "2026-08-24T01:00:00Z",
    first_seen: "2026-08-24T00:00:00Z",
    occurrences: 1,
    kind: "cumulative_reset",
    path: `rollout-${index + 1}.jsonl`,
    detail: "counter reset handled with last_token_usage"
  }));
  await page.route("**/api/v1/status", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    await route.fulfill({ response, json: { ...payload, status: { ...payload.status, warning_count: warnings.length } } });
  });
  await page.route("**/api/v1/warnings?*", async (route) => route.fulfill({ json: { items: warnings } }));

  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  await expect(page.locator("#coverageBanner")).toBeHidden();
  await expect(page.locator("#handledWarningRow")).toBeVisible();
  await expect(page.locator("#handledWarningButton")).toHaveText("4 条计数器重置已自动处理 · 查看记录");
  await page.locator("#handledWarningButton").click();
  await expect(page.locator("#warningsDialog")).toBeVisible();
  await expect(page.locator("#warningList .warning-row")).toHaveCount(4);
});

test("actionable warnings stay prominent beside quiet handled records", async ({ page }) => {
  const warnings = [
    {
      id: 1,
      created_at: "2026-08-24T01:00:00Z",
      first_seen: "2026-08-24T00:00:00Z",
      occurrences: 1,
      kind: "cumulative_reset",
      path: "handled.jsonl",
      detail: "counter reset handled with last_token_usage"
    },
    {
      id: 2,
      created_at: "2026-08-24T01:00:00Z",
      first_seen: "2026-08-24T00:00:00Z",
      occurrences: 1,
      kind: "jsonl_record",
      path: "needs-review.jsonl",
      detail: "record could not be parsed"
    }
  ];
  await page.route("**/api/v1/status", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    await route.fulfill({ response, json: { ...payload, status: { ...payload.status, warning_count: warnings.length } } });
  });
  await page.route("**/api/v1/warnings?*", async (route) => route.fulfill({ json: { items: warnings } }));

  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  await expect(page.locator("#coverageBanner")).toBeVisible();
  await expect(page.locator("#coverageText")).toHaveText("1 条去重后的数据质量记录需复核");
  await expect(page.locator("#handledWarningButton")).toHaveText("1 条计数器重置已自动处理 · 查看记录");
});

test("warning API failure keeps every status warning actionable", async ({ page }) => {
  await page.route("**/api/v1/status", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    await route.fulfill({ response, json: { ...payload, status: { ...payload.status, warning_count: 3 } } });
  });
  await page.route("**/api/v1/warnings?*", async (route) => {
    await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "warning store unavailable" }) });
  });

  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  await expect(page.locator("#coverageText")).toHaveText("3 条去重后的数据质量记录需复核");
  await expect(page.locator("#handledWarningRow")).toBeHidden();
});

test("malformed warning payload keeps every status warning actionable", async ({ page }) => {
  await page.route("**/api/v1/status", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    await route.fulfill({ response, json: { ...payload, status: { ...payload.status, warning_count: 2 } } });
  });
  await page.route("**/api/v1/warnings?*", async (route) => {
    await route.fulfill({ json: { items: { kind: "cumulative_reset" } } });
  });

  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  await expect(page.locator("#coverageText")).toHaveText("2 条去重后的数据质量记录需复核");
  await expect(page.locator("#handledWarningRow")).toBeHidden();
});

test("truncated warning list keeps every status warning actionable", async ({ page }) => {
  const truncated = Array.from({ length: 500 }, (_, index) => ({ id: index + 1, kind: "cumulative_reset" }));
  await page.route("**/api/v1/status", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    await route.fulfill({ response, json: { ...payload, status: { ...payload.status, warning_count: 501 } } });
  });
  await page.route("**/api/v1/warnings?*", async (route) => {
    await route.fulfill({ json: { items: truncated } });
  });

  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  await expect(page.locator("#coverageText")).toHaveText("501 条去重后的数据质量记录需复核");
  await expect(page.locator("#handledWarningRow")).toBeHidden();
});

test("stale status response cannot overwrite newer status UI", async ({ page }) => {
  let statusRequests = 0;
  let warningRequests = 0;
  let notifyFirstStarted;
  let releaseFirst;
  const firstStarted = new Promise((resolve) => { notifyFirstStarted = resolve; });
  const firstGate = new Promise((resolve) => { releaseFirst = resolve; });
  const actionable = [{ id: 1, kind: "jsonl_record" }];
  const handled = [{ id: 2, kind: "cumulative_reset" }];

  await page.route("**/api/v1/status", async (route) => {
    const sequence = ++statusRequests;
    const response = await route.fetch();
    const payload = await response.json();
    if (sequence === 1) {
      notifyFirstStarted();
      await firstGate;
    }
    const newer = sequence !== 1;
    await route.fulfill({
      response,
      json: {
        ...payload,
        version: newer ? "newer-version" : "stale-version",
        status: {
          ...payload.status,
          data_revision: newer ? "newer-revision" : "stale-revision",
          warning_count: 1,
          machine: {
            ...payload.status?.machine,
            id: newer ? "newer-machine-id" : "stale-machine-id",
            label: newer ? "newer-machine" : "stale-machine"
          }
        }
      }
    });
  });
  await page.route("**/api/v1/warnings?*", async (route) => {
    warningRequests += 1;
    await route.fulfill({ json: { items: warningRequests === 1 ? actionable : handled } });
  });

  await page.goto(dashboardURL, { waitUntil: "domcontentloaded" });
  await firstStarted;
  await page.locator("#localeButton").click();
  await expect(page.locator("#machineLabel")).toHaveText("newer-machine");
  await expect(page.locator("#versionLabel")).toHaveText("vnewer-version");
  await expect(page.locator("#coverageText")).toHaveText("1 deduplicated data-quality records need review");
  await expect(page.locator("#handledWarningRow")).toBeHidden();

  releaseFirst();
  await page.waitForLoadState("networkidle");

  await expect(page.locator("#machineLabel")).toHaveText("newer-machine");
  await expect(page.locator("#versionLabel")).toHaveText("vnewer-version");
  await expect(page.locator("#coverageText")).toHaveText("1 deduplicated data-quality records need review");
  await expect(page.locator("#handledWarningRow")).toBeHidden();
});

test("hourly usage inspects the selected point and navigates across local days", async ({ page }) => {
  const hourlyRequests = [];
  const hourlyCostRequests = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/timeseries" && url.searchParams.get("bucket") === "hour") hourlyRequests.push(url);
    if (url.pathname === "/api/v1/cost-estimate" && url.searchParams.has("since") && url.searchParams.has("until")
      && Date.parse(url.searchParams.get("until")) - Date.parse(url.searchParams.get("since")) === 60 * 60 * 1000) hourlyCostRequests.push(url);
  });
  await page.goto(dashboardURL, { waitUntil: "networkidle" });
  const trend = page.locator("#usageTrendPanel");
  await trend.getByRole("tab", { name: "每小时" }).click();
  await expect(page.getByRole("heading", { name: "每小时Token用量" })).toBeVisible();
  await expect(trend.getByText("最近 60 分钟", { exact: true })).toHaveCount(0);
  await expect(trend.getByText("上一小时", { exact: true })).toHaveCount(0);
  await expect(page.locator("#hourPointInspector")).toHaveCount(0);
  await expect(page.locator("#hourlyDatePicker")).toHaveValue(/^\d{4}-\d{2}-\d{2}$/);
  await expect(page.locator("#hourlyDateYear")).not.toHaveText("—");
  await expect(page.locator("#hourlyDateLabel")).not.toHaveText("—");
  const dateType = await page.locator("#hourlyDateLabel").evaluate((node) => ({
    year: Number.parseFloat(getComputedStyle(document.querySelector("#hourlyDateYear")).fontSize),
    date: Number.parseFloat(getComputedStyle(node).fontSize)
  }));
  expect(dateType.year).toBeGreaterThanOrEqual(11);
  expect(dateType.date).toBeGreaterThanOrEqual(16);
  await expect(page.locator("#hourlyTotal")).toHaveText("60");
  await expect(page.locator("#hourlyCost")).toHaveText(/^\$/);
  await expect(page.locator("#hourlyModels").getByText("gpt-5.4", { exact: true })).toBeVisible();
  await expect(page.locator("#hourlyCostCoverage")).toHaveText("100.0%");
  await expect(page.locator("#hourlyContext").getByText("不是真实账单", { exact: false })).toBeVisible();
  const summaryType = await page.evaluate(() => ({
    window: Number.parseFloat(getComputedStyle(document.querySelector("#hourlyWindowLabel")).fontSize),
    ledger: Number.parseFloat(getComputedStyle(document.querySelector("#hourlyInput")).fontSize)
  }));
  expect(summaryType.window).toBeGreaterThanOrEqual(15);
  expect(summaryType.ledger).toBeGreaterThanOrEqual(16);
  await expect(page.locator("#hourlyLine .hour-line-path")).toHaveCount(1);
  const hourPoints = page.locator("#hourlyPoints .hour-point");
  const expectedHours = await page.evaluate(() => new Date().getHours() || 24);
  await expect(hourPoints).toHaveCount(expectedHours);
  await expect(hourPoints.last()).toHaveClass(/selected/);
  await hourPoints.first().focus();
  await expect(hourPoints.first()).toHaveAttribute("aria-pressed", "true");
  if (expectedHours > 1) {
    await expect(page.locator("#hourlyTotal")).toHaveText("0");
    await expect(page.locator("#hourlyCost")).toHaveText("$0.00");
    await expect(page.locator("#hourlyModels")).toContainText("没有模型用量");
  }
  await hourPoints.first().press("End");
  await expect(hourPoints.last()).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator("#hourlyTotal")).toHaveText("60");
  const alignment = await page.evaluate(() => {
    const svg = document.querySelector("#hourlyLine");
    const polyline = svg.querySelector(".hour-line-path");
    const svgRect = svg.getBoundingClientRect();
    const coordinates = polyline.getAttribute("points").trim().split(/\s+/).map((pair) => pair.split(",").map(Number));
    return [...document.querySelectorAll("#hourlyPoints .hour-point")].map((button, index) => {
      const rect = button.getBoundingClientRect();
      const [x, y] = coordinates[index];
      return {
        x: Math.abs(rect.left + rect.width / 2 - (svgRect.left + x / 1000 * svgRect.width)),
        y: Math.abs(rect.top + rect.height / 2 - (svgRect.top + y / 180 * svgRect.height))
      };
    });
  });
  expect(Math.max(...alignment.map(({ x }) => x))).toBeLessThan(0.5);
  expect(Math.max(...alignment.map(({ y }) => y))).toBeLessThan(0.5);
  await expect(page.locator("#hourlyWindowLabel")).not.toHaveText("—");

  const initialDate = await page.locator("#hourlyDatePicker").inputValue();
  await page.locator("#previousHourDay").click();
  const previousDate = await page.locator("#hourlyDatePicker").inputValue();
  expect(previousDate).toBe(await page.evaluate((value) => {
    const [year, month, day] = value.split("-").map(Number);
    const date = new Date(year, month - 1, day);
    date.setDate(date.getDate() - 1);
    return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
  }, initialDate));
  await expect(hourPoints).toHaveCount(24);
  await expect(hourPoints.last()).toHaveClass(/selected/);
  await expect(page.locator("#currentHourDay")).toBeEnabled();
  const arbitraryDate = await page.evaluate((value) => {
    const [year, month, day] = value.split("-").map(Number);
    const date = new Date(year, month - 1, day);
    date.setDate(date.getDate() - 8);
    return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
  }, initialDate);
  await page.locator("#hourlyDatePicker").fill(arbitraryDate);
  await page.locator("#hourlyDatePicker").dispatchEvent("change");
  await expect(page.locator("#hourlyDatePicker")).toHaveValue(arbitraryDate);
  await expect(page.locator("#trendHourlyPane")).not.toHaveAttribute("aria-busy", "true");
  await expect(hourPoints).toHaveCount(24);
  await page.locator("#currentHourDay").click();
  await expect(page.locator("#hourlyDatePicker")).toHaveValue(await page.evaluate(() => {
    const now = new Date();
    return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
  }));
  await expect(page.locator("#currentHourDay")).toBeDisabled();
  expect(hourlyRequests.some((url) => Date.parse(url.searchParams.get("until")) - Date.parse(url.searchParams.get("since")) === 24 * 60 * 60 * 1000)).toBeTruthy();
  expect(hourlyCostRequests.length).toBeGreaterThan(0);
});

test("view switching exposes the target panel immediately", async ({ page }) => {
  await page.goto(dashboardURL, { waitUntil: "networkidle" });
  const elapsed = await page.evaluate(async () => {
    const panel = document.querySelector("#view-daily");
    const start = performance.now();
    document.querySelector('[data-view="daily"]').click();
    if (!panel.hidden) return performance.now() - start;
    await new Promise((resolve) => {
      const observer = new MutationObserver(() => {
        if (panel.hidden) return;
        observer.disconnect();
        resolve();
      });
      observer.observe(panel, { attributes: true, attributeFilter: ["hidden"] });
    });
    return performance.now() - start;
  });
  expect(elapsed).toBeLessThan(100);
  await expect(page.locator("#view-daily")).toBeVisible();
});

test("details renders breakdown before delayed sessions and dimension changes stay local", async ({ page }) => {
  let sessionRequests = 0;
  let estimateRequests = 0;
  await page.route("**/api/v1/sessions?**", async (route) => {
    sessionRequests++;
    expect(new URL(route.request().url()).searchParams.get("include_estimate")).toBe("0");
    await delay(1200);
    await route.continue();
  });
  await page.route("**/api/v1/session-estimates?**", async (route) => {
    estimateRequests++;
    await delay(1200);
    await route.continue();
  });
  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  const started = Date.now();
  await page.getByRole("tab", { name: "明细" }).click();
  await expect(page.locator("#detailBreakdown .breakdown-row").first()).toBeVisible({ timeout: 1000 });
  expect(Date.now() - started).toBeLessThan(1100);
  await expect(page.locator("#sessionRows")).toContainText("正在加载本机 Session");
  await expect(page.locator("#sessionRows .session-row").first()).toBeVisible({ timeout: 3000 });
  await expect(page.locator("#sessionRows .session-cost").first()).toHaveClass(/pending/);
  await expect(page.locator("#sessionRows .session-cost").first()).not.toHaveClass(/pending/, { timeout: 3000 });
  expect(estimateRequests).toBe(1);

  const requestsBeforeDimensionChange = sessionRequests;
  const sourceBreakdown = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/api/v1/breakdown" && url.searchParams.get("dimension") === "source";
  });
  await page.getByRole("tab", { name: "来源" }).click();
  await sourceBreakdown;
  await expect(page.locator("#breakdownTitle")).toHaveText("按来源");
  await page.waitForTimeout(100);
  expect(sessionRequests).toBe(requestsBeforeDimensionChange);
});

test("details supports today and an arbitrary local date from both date controls", async ({ page }) => {
  await page.goto(dashboardURL, { waitUntil: "networkidle" });
  await page.getByRole("tab", { name: "明细" }).click();

  const dates = await page.evaluate(() => {
    const key = (date) => `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
    const today = new Date();
    const tomorrow = new Date(today);
    const selected = new Date(today);
    tomorrow.setDate(tomorrow.getDate() + 1);
    selected.setDate(selected.getDate() - 10);
    return { today: key(today), tomorrow: key(tomorrow), selected: key(selected) };
  });

  const todayResponse = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/api/v1/breakdown" && url.searchParams.get("since") === dates.today && url.searchParams.get("until") === dates.tomorrow;
  });
  await page.locator('[data-detail-range="today"]').click();
  await todayResponse;
  await expect(page.locator('[data-detail-range="today"]')).toHaveAttribute("aria-pressed", "true");

  const pickerResponse = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/api/v1/breakdown" && url.searchParams.get("date") === dates.selected;
  });
  await page.locator("#detailDatePicker").fill(dates.selected);
  await page.locator("#detailDatePicker").dispatchEvent("change");
  const picked = new URL((await pickerResponse).url());
  expect(picked.searchParams.has("since")).toBeFalsy();
  expect(picked.searchParams.has("until")).toBeFalsy();
  await expect(page.locator("#detailDatePicker")).toHaveValue(dates.selected);
  await expect(page.locator("[data-detail-range][aria-pressed=true]")).toHaveCount(0);
  await expect(page.locator(".filter-chip")).toContainText("日期 ·");

  await page.locator("#filterButton").click();
  await expect(page.locator("#filterDate")).toHaveValue(dates.selected);
  const filterResponse = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/api/v1/breakdown" && url.searchParams.get("date") === dates.today;
  });
  await page.locator("#filterDate").fill(dates.today);
  await page.locator("#applyFilters").click();
  await filterResponse;
  await expect(page.locator("#detailDatePicker")).toHaveValue(dates.today);

  const presetResponse = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/api/v1/breakdown" && url.searchParams.get("date") === null && url.searchParams.get("since") !== null;
  });
  await page.locator('[data-detail-range="7d"]').click();
  await presetResponse;
  await expect(page.locator('[data-detail-range="7d"]')).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator("#detailDatePicker")).toHaveValue("");
  await expect(page.locator(".filter-chip")).toHaveCount(0);
});

test("filter dimensions load lazily once instead of running startup breakdowns", async ({ page }) => {
  const dimensionRequests = [];
  const startupBreakdowns = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/dimensions") dimensionRequests.push(request.url());
    if (url.pathname === "/api/v1/breakdown") startupBreakdowns.push(request.url());
  });
  await page.goto(dashboardURL, { waitUntil: "networkidle" });
  expect(dimensionRequests).toHaveLength(0);
  expect(startupBreakdowns).toHaveLength(0);

  await page.locator("#filterButton").click();
  await expect.poll(() => dimensionRequests.length).toBe(1);
  await expect(page.locator("#filterModel option")).toHaveCount(2);
  await page.locator("#filterSheet [data-close]").click();
  await page.locator("#filterButton").click();
  await page.waitForTimeout(100);
  expect(dimensionRequests).toHaveLength(1);
});

test("display settings improve the default scale and persist font, density, theme, and motion", async ({ page }) => {
  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  await expect(page.locator("html")).toHaveAttribute("data-font-size", "comfortable");
  await expect(page.locator("html")).toHaveAttribute("data-density", "balanced");
  await page.locator("#settingsButton").click();
  await expect(page.getByRole("heading", { name: "显示设置" })).toBeVisible();
  await expect(page.locator('input[name="fontSize"][value="comfortable"]')).toBeChecked();
  await expect(page.locator('input[name="density"][value="balanced"]')).toBeChecked();

  await page.locator('input[name="fontSize"][value="large"]').check({ force: true });
  await page.locator('input[name="density"][value="compact"]').check({ force: true });
  await page.locator('input[name="theme"][value="dark"]').check({ force: true });
  await page.locator('input[name="motion"][value="reduce"]').check({ force: true });
  await expect(page.locator("html")).toHaveAttribute("data-font-size", "large");
  await expect(page.locator("html")).toHaveAttribute("data-density", "compact");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator("html")).toHaveAttribute("data-motion", "reduce");
  expect(await page.evaluate(() => getComputedStyle(document.body).fontSize)).toBe("16.875px");
  expect(await page.evaluate(() => JSON.parse(localStorage.getItem("codex-usage-display-preferences")))).toEqual({
    fontSize: "large",
    density: "compact",
    theme: "dark",
    motion: "reduce"
  });

  await page.locator("#settingsDialog [data-close]").first().click();
  await page.reload({ waitUntil: "networkidle" });
  await expect(page.locator("html")).toHaveAttribute("data-font-size", "large");
  await expect(page.locator("html")).toHaveAttribute("data-density", "compact");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator("html")).toHaveAttribute("data-motion", "reduce");

  await page.locator("#settingsButton").click();
  await page.locator("#resetSettings").click();
  await expect(page.locator("html")).toHaveAttribute("data-font-size", "comfortable");
  await expect(page.locator("html")).toHaveAttribute("data-density", "balanced");
  expect(await page.evaluate(() => ({
    theme: document.documentElement.getAttribute("data-theme"),
    motion: document.documentElement.getAttribute("data-motion"),
    saved: localStorage.getItem("codex-usage-display-preferences")
  }))).toEqual({ theme: null, motion: null, saved: null });
});

test("navigation, month drill-down, filter chips, pricing, and scan feedback work", async ({ page }) => {
  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  const dailyTab = page.locator(".primary-nav").getByRole("tab", { name: "每日" });
  await dailyTab.focus();
  await dailyTab.press("Enter");
  await expect(page.getByRole("heading", { name: "每日用量" })).toBeVisible();
  const initialMonth = await page.locator("#monthLabel").textContent();
  await page.locator("#previousMonth").click();
  await expect(page.locator("#monthLabel")).not.toHaveText(initialMonth);
  await expect(page.locator("[data-calendar-date]").first()).toBeVisible();

  const directDate = "2025-04-17";
  await page.locator("#dailyDatePicker").fill(directDate);
  await page.locator("#dailyDatePicker").dispatchEvent("change");
  await expect(page.locator("#dailyDatePicker")).toHaveValue(directDate);
  await expect(page.locator("#dailyDateYear")).toContainText("2025");
  await expect(page.locator("#dailyDateLabel")).toContainText("4月17日");
  await expect(page.locator(`[data-calendar-date="${directDate}"]`)).toHaveClass(/selected/);
  await expect(page.locator("#selectedDateTitle")).toContainText("4月17日");

  await page.getByRole("tab", { name: "明细" }).click();
  await expect(page.getByRole("heading", { name: "明细与归属" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Session 明细" })).toBeVisible();
  await expect(page.getByText("等价 API 价格", { exact: true })).toBeVisible();
  await expect(page.locator(".session-cost").first()).not.toHaveClass(/pending/);
  await expect(page.locator(".session-cost strong").first()).toHaveText(/^\$/);

  const modelDrill = page.locator('[data-drill-value="gpt-5.4"]');
  await modelDrill.click();
  await expect(page.locator('[data-drill-value="gpt-5.4"]')).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".filter-chip")).toContainText("模型 · gpt-5.4");
  await page.locator('[data-drill-value="gpt-5.4"]').click();
  await expect(page.locator(".filter-chip")).toHaveCount(0);

  await page.locator("#sessionSearch").fill("codex-usage-e2e");
  await expect(page.locator(".session-row")).toHaveCount(1);
  await page.locator("#sessionSearch").fill("no-such-session");
  await expect(page.getByText("没有匹配的 Session")).toBeVisible();
  await page.locator("#sessionSearchClear").click();
  await expect(page.locator(".session-row")).toHaveCount(1);

  await page.locator(".session-filter").click();
  await expect(page.locator(".filter-chip")).toContainText("Session · e2e-session");
  await expect(page.locator(".session-filter")).toHaveText("取消");
  await page.locator(".session-filter").click();
  await expect(page.locator(".filter-chip")).toHaveCount(0);

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
  await page.goto(dashboardURL, { waitUntil: "networkidle" });

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

  await page.locator(".primary-nav").getByRole("tab", { name: "每日" }).click();
  await expect(page.getByRole("heading", { name: "每日用量" })).toBeVisible();
  const monthCostKey = dataRequests.find((item) => item.startsWith("/api/v1/cost-estimate?since=") && item.includes("&bucket=day"));
  expect(monthCostKey).toBeTruthy();
  const firstMonthCount = dataRequests.filter((item) => item === monthCostKey).length;
  await page.getByRole("tab", { name: "明细" }).click();
  await expect(page.getByRole("heading", { name: "明细与归属" })).toBeVisible();
  await page.locator(".primary-nav").getByRole("tab", { name: "每日" }).click();
  await expect(page.getByRole("heading", { name: "每日用量" })).toBeVisible();
  await page.waitForTimeout(100);
  expect(dataRequests.filter((item) => item === monthCostKey).length).toBe(firstMonthCount);
});

test("historical rebuild requires explicit dashboard approval", async ({ page }) => {
  const requests = [];
  await page.route("**/api/v1/rescan", async (route) => {
    const payload = route.request().postDataJSON();
    requests.push(payload);
    if (!payload.rebuild) {
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({
          error: "现有统计已保留，需要用户确认后才能重建",
          rebuild_required: true,
          kind: "rollout_truncated",
          detail: "文件从 1024 缩短到 512"
        })
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ homes: 1, files: 1, events_inserted: 1, duplicates: 0, warnings: 0 })
    });
  });
  await page.goto(dashboardURL);

  await page.locator("#scanButton").click();
  await expect(page.locator("#rebuildDialog")).toBeVisible();
  await expect(page.locator("#rebuildDetail")).toHaveText("文件从 1024 缩短到 512");
  await page.getByRole("button", { name: "保留现有统计" }).click();
  await expect(page.locator("#rebuildDialog")).toBeHidden();
  expect(requests).toEqual([{ rebuild: false }]);

  await page.locator("#scanButton").click();
  await page.locator("#confirmRebuild").click();
  await expect(page.getByText(/扫描完成：新增/)).toBeVisible();
  expect(requests).toEqual([{ rebuild: false }, { rebuild: false }, { rebuild: true }]);
});

test("mobile, tablet, themes, and reduced motion avoid page overflow", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(dashboardURL, { waitUntil: "networkidle" });
  await page.locator("#usageTrendPanel").getByRole("tab", { name: "每小时" }).click();
  await expect(page.locator("#hourlyPoints .hour-point")).toHaveCount(await page.evaluate(() => new Date().getHours() || 24));
  await expect.poll(() => page.locator(".hourly-chart-scroll").evaluate((node) => node.scrollLeft)).toBeGreaterThan(0);
  await page.getByRole("tab", { name: "明细" }).click();
  await expect(page.getByRole("heading", { name: "Session 明细" })).toBeVisible();
  await expect(page.locator("#sessionRows .session-row").first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await expect(page.locator("#exportButton")).toBeVisible();
  await expect(page.locator("#settingsButton")).toBeVisible();
  await page.locator("#settingsButton").click();
  await expect(page.locator(".settings-group")).toHaveCount(5);
  expect(await page.locator("#settingsDialog .dialog-frame").evaluate((dialog) => dialog.scrollWidth <= dialog.clientWidth)).toBe(true);
  await page.locator("#settingsDialog [data-close]").first().click();
  await expect(page.locator("#settingsDialog")).toBeHidden();

  await page.setViewportSize({ width: 1024, height: 900 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(1024);
  await page.getByRole("tab", { name: "概览" }).click();
  await page.locator("#themeButton").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", /light|dark/);

  await page.emulateMedia({ reducedMotion: "reduce" });
  const duration = await page.locator(".pressable").first().evaluate((node) => parseFloat(getComputedStyle(node).transitionDuration));
  expect(duration).toBeLessThanOrEqual(.001);
});

test("mutation endpoints require the same loopback origin", async ({ request }) => {
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
  const otherLoopbackPort = await request.post(`${baseURL}/api/v1/rescan`, {
    headers: { Origin: `${baseURL.slice(0, baseURL.lastIndexOf(":"))}:49999` },
    data: {}
  });
  expect(otherLoopbackPort.status()).toBe(403);
  const crossSiteWithoutOrigin = await request.post(`${baseURL}/api/v1/rescan`, {
    headers: { "Sec-Fetch-Site": "cross-site" },
    data: {}
  });
  expect(crossSiteWithoutOrigin.status()).toBe(403);
});

test("localization catalogs, precedence, persistence, dates, numbers, and ARIA stay aligned", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("codex-usage-locale", "zh-CN"));
  await page.goto(`${baseURL}/?lang=en`, { waitUntil: "networkidle" });

  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.getByRole("heading", { name: "Per-machine usage overview" })).toBeVisible();
  await expect(page.locator("#machinePill")).toHaveAttribute("aria-label", /Current machine:/);
  await expect(page.locator("#overviewSubtitle")).toHaveText("Past 7 local calendar days");
  await expect(page.locator("#overviewTotal")).toContainText(/\d/);
  await expect(page.locator("#exportButton")).toHaveAttribute("aria-controls", "exportDialog");
  await expect(page.locator("#pricingButton")).toHaveAttribute("aria-controls", "pricingDialog");
  await expect(page.locator("#warningButton")).toHaveAttribute("aria-controls", "warningsDialog");
  const catalogs = await page.evaluate(() => {
    const values = window.CodexUsageI18n.catalogs;
    const zh = Object.keys(values["zh-CN"]).sort();
    const en = Object.keys(values.en).sort();
    return { equal: JSON.stringify(zh) === JSON.stringify(en), count: zh.length };
  });
  expect(catalogs.equal).toBe(true);
  expect(catalogs.count).toBeGreaterThan(150);
  for (const leaked of ["本机用量概览", "当前筛选", "定价设置", "数据来源与归属"]) {
    await expect(page.getByText(leaked, { exact: true })).toHaveCount(0);
  }

  await page.locator(".primary-nav").getByRole("tab", { name: "Daily" }).click();
  await expect(page.locator("#monthLabel")).toContainText(/[A-Za-z]/);
  await page.locator("#localeButton").click();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await expect(page.getByRole("heading", { name: "每日用量" })).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem("codex-usage-locale"))).toBe("zh-CN");
  expect(new URL(page.url()).searchParams.get("lang")).toBe("zh-CN");
});
