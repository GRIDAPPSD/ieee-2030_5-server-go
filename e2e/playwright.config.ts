import { defineConfig } from '@playwright/test';

// The specs call the self-signed admin and device endpoints from Node. Set
// here rather than on a command line so every shell, Windows included, gets it.
process.env.NODE_TLS_REJECT_UNAUTHORIZED ??= '0';

export default defineConfig({
  // #209: build sep2server once before any worker starts to prevent
  // ETXTBSY when concurrent workers raced to `go build -o sep2server`.
  globalSetup: require.resolve('./global-setup.ts'),
  testDir: '.',
  timeout: 30000,
  retries: 0,
  use: {
    // Admin dashboard runs on HTTPS with self-signed cert
    ignoreHTTPSErrors: true,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { browserName: 'chromium' },
    },
  ],
});
