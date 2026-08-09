import { test, expect } from "@playwright/test";
import { mkdir, mkdtemp, writeFile, rm } from "node:fs/promises";
import { once } from "node:events";
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
  if (testBinaryDir) await rm(testBinaryDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
});

test("overview is calm, local-only, and exposes honest cost coverage", async ({ page }) => {
  const consoleErrors = [];
  const externalRequests = [];
  page.on("console", (message) => { if (message.type() === "error") consoleErrors.push(message.text()); });
  page.on("pageerror", (error) => consoleErrors.push(error.message));
  page.on("request", (request) => {
    if (new URL(request.url()).origin !== baseURL) externalRequests.push(request.url());
  });
  await page.goto(dashboardURL, { waitUntil: "networkidle" });

  await expect(page.getByRole("heading", { name: "本机用量概览" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "概览" })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("过去 7 个本地自然日")).toBeVisible();
  await expect(page.getByRole("heading", { name: "每日脉冲带" })).toBeVisible();
  await expect(page.locator("#view-overview").getByText("Standard API 等价成本", { exact: true })).toBeVisible();
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

  const dailyTab = page.getByRole("tab", { name: "每日" });
  await dailyTab.focus();
  await dailyTab.press("Enter");
  await expect(page.getByRole("heading", { name: "每日用量" })).toBeVisible();
  const initialMonth = await page.locator("#monthLabel").textContent();
  await page.locator("#previousMonth").click();
  await expect(page.locator("#monthLabel")).not.toHaveText(initialMonth);
  await expect(page.locator("[data-calendar-date]").first()).toBeVisible();

  await page.getByRole("tab", { name: "明细" }).click();
  await expect(page.getByRole("heading", { name: "明细与归属" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Session 明细" })).toBeVisible();
  await expect(page.getByText("等价 API 价格", { exact: true })).toBeVisible();
  await expect(page.locator(".session-cost strong").first()).not.toHaveText("—");

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
  await page.getByRole("tab", { name: "明细" }).click();
  await expect(page.getByRole("heading", { name: "Session 明细" })).toBeVisible();
  expect(await page.locator(".session-row").count()).toBeGreaterThan(0);
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

  await page.getByRole("tab", { name: "Daily" }).click();
  await expect(page.locator("#monthLabel")).toContainText(/[A-Za-z]/);
  await page.locator("#localeButton").click();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await expect(page.getByRole("heading", { name: "每日用量" })).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem("codex-usage-locale"))).toBe("zh-CN");
  expect(new URL(page.url()).searchParams.get("lang")).toBe("zh-CN");
});
