import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  // multi-node.spec.ts spawns real OS processes and forces its own
  // describe.configure({ mode: 'serial' }); a generous global test timeout
  // covers real binary build + process spawn + browser automation without
  // resorting to fixed sleeps inside the harness itself.
  timeout: 90_000,
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: 'list',
  use: {
    baseURL:
      process.env.PLAYWRIGHT_BASE_URL || 'http://localhost:5173',
    headless: true,
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  webServer: {
    command: 'npm run dev',
    url: 'http://localhost:5173',
    reuseExistingServer: true,
  },
})
