/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// The shell is mounted at /ui/ on the admin listener (internal/server/spa.go),
// alongside the existing dashboard at "/", so base must match that mount
// point or every built asset URL resolves one level too high.
//
// Build output lands in ../dist (internal/server/web/dist), the directory
// embed.go embeds via "//go:embed all:dist".
//
// The test block runs vitest in jsdom so component tests exercise rendered
// DOM output, not just non-throw; it shares this config file rather than a
// separate vitest.config.ts so the svelte plugin setup is not duplicated.
export default defineConfig({
  base: '/ui/',
  plugins: [svelte()],
  build: {
    outDir: '../dist',
    emptyOutDir: true,
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
    globals: false,
    // Spies on the api module are created per test with vi.spyOn; without
    // this, one test's mocked fetch stays installed for the next file in
    // the same worker and a "was never called" assertion sees the
    // previous test's calls.
    restoreMocks: true,
  },
  resolve: process.env.VITEST
    ? {
        // Vitest runs in Node, where Vite's default export-condition
        // resolution picks Svelte's server (SSR) build, which has no
        // mount()/onMount lifecycle. The browser condition forces the
        // client build so component tests exercise the same runtime the
        // server actually serves.
        conditions: ['browser'],
      }
    : undefined,
})
