import { chromium } from "@playwright/test";
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { once } from "node:events";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { spawn, spawnSync } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const mediaDir = path.join(repoRoot, "docs", "media");
const imagesDir = path.join(repoRoot, "docs", "images");
const tempDir = await mkdtemp(path.join(tmpdir(), "codex-usage-media-"));
const port = 47000 + (process.pid % 1000);
const baseURL = `http://127.0.0.1:${port}/codex-usage/`;

function run(command, args) {
  const result = spawnSync(command, args, { cwd: repoRoot, stdio: "inherit", windowsHide: true });
  if (result.status !== 0) throw new Error(`${command} failed with exit code ${result.status}`);
}

function assertSynthetic(text) {
  const forbidden = [/[A-Z]:\\Users\\/i, /\/home\/[a-z0-9._-]+\//i, /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i];
  for (const pattern of forbidden) {
    if (pattern.test(text)) throw new Error(`Sensitive-looking value matched ${pattern}`);
  }
}

async function waitForServer() {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    try {
      if ((await fetch(baseURL)).ok) return;
    } catch {}
    await delay(100);
  }
  throw new Error("media demo server did not become ready");
}

async function recordDemo(browser, locale, output) {
  const videoDir = path.join(tempDir, locale);
  await mkdir(videoDir, { recursive: true });
  const context = await browser.newContext({ viewport: { width: 1280, height: 720 }, recordVideo: { dir: videoDir, size: { width: 1280, height: 720 } } });
  const page = await context.newPage();
  await page.goto(`${baseURL}?lang=${locale}`, { waitUntil: "networkidle" });
  assertSynthetic(await page.locator("body").innerText());
  const video = page.video();
  await delay(2_500);
  await page.locator('[data-overview-range="30d"]').click();
  await delay(2_500);
  await page.getByRole("tab", { name: locale === "en" ? "Daily" : "每日" }).click();
  await delay(3_000);
  const activeDay = page.locator('[data-calendar-date]:not(.zero)').last();
  if (await activeDay.count()) await activeDay.click();
  await delay(3_000);
  await page.getByRole("tab", { name: locale === "en" ? "Details" : "明细" }).click();
  await delay(2_500);
  await page.locator('[data-dimension="project"]').click();
  await delay(2_000);
  await page.locator("#filterButton").click();
  await delay(1_200);
  await page.locator("#filterAgent").selectOption("subagent");
  await page.locator("#applyFilters").click();
  await delay(3_000);
  await page.getByRole("tab", { name: locale === "en" ? "Overview" : "概览" }).click();
  await delay(2_000);
  await page.locator("#themeButton").click();
  await delay(2_500);
  assertSynthetic(await page.locator("body").innerText());
  await page.close();
  await context.close();
  await video.saveAs(output);
}

run(process.execPath, ["scripts/build-demo.mjs", "dist/pages"]);
await mkdir(mediaDir, { recursive: true });
const server = spawn(process.execPath, ["scripts/serve-static.mjs", "dist/pages", String(port), "codex-usage"], { cwd: repoRoot, stdio: "ignore", windowsHide: true });

try {
  await waitForServer();
  const browser = await chromium.launch({ headless: true });
  try {
    const socialContext = await browser.newContext({ viewport: { width: 1280, height: 640 }, deviceScaleFactor: 1 });
    const social = await socialContext.newPage();
    await social.goto(pathToFileURL(path.join(repoRoot, "scripts", "social-preview.html")).href, { waitUntil: "load" });
    assertSynthetic(await social.locator("body").innerText());
    await social.screenshot({ path: path.join(mediaDir, "social-preview.png") });
    await socialContext.close();

    const desktopContext = await browser.newContext({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1 });
    const desktop = await desktopContext.newPage();
    await desktop.goto(`${baseURL}?lang=zh-CN`, { waitUntil: "networkidle" });
    assertSynthetic(await desktop.locator("body").innerText());
    await desktop.screenshot({ path: path.join(imagesDir, "dashboard.png"), fullPage: true });
    await desktop.setViewportSize({ width: 390, height: 844 });
    await desktop.screenshot({ path: path.join(imagesDir, "dashboard-mobile.png"), fullPage: true });
    await desktopContext.close();

    await recordDemo(browser, "zh-CN", path.join(tempDir, "demo-zh.webm"));
    await recordDemo(browser, "en", path.join(tempDir, "demo-en.webm"));
  } finally {
    await browser.close();
  }

  run("ffmpeg", ["-y", "-i", path.join(tempDir, "demo-zh.webm"), "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-an", path.join(mediaDir, "codex-usage-demo-zh.mp4")]);
  run("ffmpeg", ["-y", "-i", path.join(tempDir, "demo-en.webm"), "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-an", path.join(mediaDir, "codex-usage-demo-en.mp4")]);
  run("ffmpeg", ["-y", "-t", "14", "-i", path.join(tempDir, "demo-zh.webm"), "-filter_complex", "fps=10,scale=960:-1:flags=lanczos,split[s0][s1];[s0]palettegen=stats_mode=diff[p];[s1][p]paletteuse=dither=bayer:bayer_scale=5", "-loop", "0", path.join(mediaDir, "codex-usage-demo.gif")]);

  for (const file of ["social-preview.png", "codex-usage-demo-zh.mp4", "codex-usage-demo-en.mp4", "codex-usage-demo.gif"]) {
    const content = await readFile(path.join(mediaDir, file));
    if (content.length < 10_000) throw new Error(`${file} is unexpectedly small`);
  }
  console.log(`Generated launch media in ${mediaDir}`);
} finally {
  if (server.exitCode === null && server.signalCode === null) {
    const exited = once(server, "exit");
    server.kill();
    await Promise.race([exited, delay(5_000)]);
  }
  await rm(tempDir, { recursive: true, force: true });
}
