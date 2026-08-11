import { test, expect } from "@playwright/test";
import { once } from "node:events";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";

let server;
let baseURL;

test.beforeAll(async () => {
  const port = 46000 + (process.pid % 1000);
  baseURL = `http://127.0.0.1:${port}/codex-usage/`;
  server = spawn(process.execPath, ["scripts/serve-static.mjs", "dist/pages", String(port), "codex-usage"], { stdio: "ignore", windowsHide: true });
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(baseURL);
      if (response.ok) return;
    } catch {}
    await delay(100);
  }
  throw new Error("synthetic demo server did not become ready");
});

test.afterAll(async () => {
  if (server && server.exitCode === null && server.signalCode === null) {
    const exited = once(server, "exit");
    server.kill();
    await Promise.race([exited, delay(5_000)]);
  }
});

test("Pages subpath loads the canonical UI with synthetic-only APIs", async ({ page }) => {
  const external = [];
  const networkAPIs = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.origin !== new URL(baseURL).origin) external.push(request.url());
    if (url.pathname.includes("/api/v1/")) networkAPIs.push(request.url());
  });
  await page.goto(`${baseURL}?lang=en`, { waitUntil: "networkidle" });

  await expect(page.getByText("INTERACTIVE DEMO / SYNTHETIC DATA")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Per-machine usage overview" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.locator("#machineLabel")).toHaveText("Synthetic Windows · demo");
  const trend = page.locator("#usageTrendPanel");
  await expect(trend.getByRole("heading", { name: "Daily token usage" })).toBeVisible();
  await expect(trend.getByRole("tab", { name: "Daily" })).toHaveAttribute("aria-selected", "true");
  await trend.getByRole("tab", { name: "Hourly" }).click();
  await expect(page.getByRole("heading", { name: "Hourly token usage" })).toBeVisible();
  await expect(page.locator("#hourlyLine .hour-line-path")).toHaveCount(1);
  await expect(page.locator("#hourlyPoints .hour-point")).toHaveCount(24);
  expect(networkAPIs).toEqual([]);
  expect(external).toEqual([]);
  expect(await page.context().cookies()).toEqual([]);

  await page.locator(".primary-nav").getByRole("tab", { name: "Daily" }).click();
  await expect(page.getByRole("heading", { name: "Daily usage" })).toBeVisible();
  await expect(page.locator("[data-calendar-date]").first()).toBeVisible();
  await page.getByRole("tab", { name: "Details" }).click();
  await expect(page.getByRole("heading", { name: "Session details" })).toBeVisible();

  await page.locator("#warningButton").click();
  await expect(page.getByText("Raw diagnostic").first()).toBeVisible();
  await page.locator("#warningsDialog [data-close]").click();

  const initialSessions = await page.locator(".session-row").count();
  await page.locator("#filterButton").click();
  await page.locator("#filterAgent").selectOption("subagent");
  await page.locator("#applyFilters").click();
  await expect(page.locator(".filter-chip")).toContainText("Agent · subagent");
  await expect.poll(() => page.locator(".session-row").count()).toBeLessThan(initialSessions);

  await page.locator("#exportButton").click();
  await expect(page.locator("#exportJson")).toHaveAttribute("href", /^data:application\/json/);
  await expect(page.locator("#exportCsv")).toHaveAttribute("href", /^data:text\/csv/);
});

test("language priority, persistence, ARIA, pricing, scan, theme, and mobile layout work", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("codex-usage-locale", "zh-CN"));
  await page.goto(`${baseURL}?lang=en`, { waitUntil: "networkidle" });
  await expect(page.getByRole("heading", { name: "Per-machine usage overview" })).toBeVisible();
  await expect(page.locator("#machinePill")).toHaveAttribute("aria-label", /Current machine:/);
  await expect(page.locator("#overviewSubtitle")).toHaveText("Past 7 local calendar days");

  await page.locator("#localeButton").click();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  expect(await page.evaluate(() => localStorage.getItem("codex-usage-locale"))).toBe("zh-CN");
  expect(new URL(page.url()).searchParams.get("lang")).toBe("zh-CN");

  await page.locator("#pricingButton").click();
  await page.locator("#newOverrideModel").fill("codex-auto-review");
  await page.locator("#addOverride").click();
  const card = page.locator('[data-pricing-model="codex-auto-review"]');
  await card.locator("[data-rate-mode]").selectOption("alias");
  await card.locator("[data-alias]").selectOption("gpt-5.4");
  await page.locator("#savePricing").click();
  await expect(page.getByText("本机定价已保存，费用已重新估算")).toBeVisible();

  await page.locator("#scanButton").click();
  await expect(page.getByText(/扫描完成：新增 2 个事件/)).toBeVisible();
  await page.locator("#themeButton").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", /light|dark/);

  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await expect(page.locator("#localeButton")).toBeVisible();
});

test("saved and browser locales follow the documented fallback order", async ({ browser }) => {
  const savedContext = await browser.newContext({ locale: "en-US" });
  await savedContext.addInitScript(() => localStorage.setItem("codex-usage-locale", "zh-CN"));
  const savedPage = await savedContext.newPage();
  await savedPage.goto(baseURL, { waitUntil: "networkidle" });
  await expect(savedPage.locator("html")).toHaveAttribute("lang", "zh-CN");
  await savedContext.close();

  const browserContext = await browser.newContext({ locale: "en-US" });
  const browserPage = await browserContext.newPage();
  await browserPage.goto(baseURL, { waitUntil: "networkidle" });
  await expect(browserPage.locator("html")).toHaveAttribute("lang", "en");
  await browserContext.close();

  const fallbackContext = await browser.newContext({ locale: "fr-FR" });
  const fallbackPage = await fallbackContext.newPage();
  await fallbackPage.goto(baseURL, { waitUntil: "networkidle" });
  await expect(fallbackPage.locator("html")).toHaveAttribute("lang", "zh-CN");
  await fallbackContext.close();
});
