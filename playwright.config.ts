import { defineConfig } from '@playwright/test';

const port = process.env.UI_TEST_PORT || '3939';

export default defineConfig({
  testDir: './tests/ui',
  timeout: 30_000,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: 'on-first-retry',
  },
  webServer: {
    command: `UI_TEST_PORT=${port} go run ./cmd/driftd-ui-testserver`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
});
