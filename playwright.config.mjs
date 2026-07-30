import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  timeout: 30_000,
  workers: 1,
  use: {
    browserName: "chromium",
    headless: true,
    viewport: { width: 1440, height: 1000 }
  }
});
