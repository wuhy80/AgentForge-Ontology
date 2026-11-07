import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  timeout: 90_000,
  expect: { timeout: 10_000 },
  reporter: 'list',
  use: {
    baseURL: process.env.ONTOLOGY_WEB_URL ?? 'http://127.0.0.1:5173',
    channel: 'msedge',
    headless: true,
    actionTimeout: 10_000,
    navigationTimeout: 15_000,
    viewport: { width: 1440, height: 1000 },
    // Credentials and API responses must not be recorded in traces or videos.
    trace: 'off',
    video: 'off',
    screenshot: 'off',
  },
})
