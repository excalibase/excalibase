import { defineConfig, devices } from '@playwright/test';

// STUDIO_E2E_PORT moves the local dev server off 5173 when that port is taken.
// STUDIO_LIVE_URL points the run at a real Studio (e2e/live/*): no dev server.
const port = Number(process.env.STUDIO_E2E_PORT ?? 5173);
const localUrl = `http://localhost:${port}`;
const liveUrl = process.env.STUDIO_LIVE_URL;

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: 'html',
  use: {
    baseURL: liveUrl ?? localUrl,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  webServer: liveUrl
    ? undefined
    : {
        command: `npm run dev -- --port ${port} --strictPort`,
        url: localUrl,
        reuseExistingServer: !process.env.CI,
        timeout: 30000,
      },
});
