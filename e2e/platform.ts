import { basename, join } from 'path';
import type { test as PlaywrightTest } from '@playwright/test';

// The one definition of where the sep2server binary lives: globalSetup builds
// it here, setup.ts runs it, and specs that mint certificates exec it.
export const SERVER_BINARY = join(__dirname, '..', process.platform === 'win32' ? 'sep2server.exe' : 'sep2server');

// Specs that do not run on Windows, by file name, each with the reason shown
// in the report. Anything not listed here runs on every platform.
const LOG_WRITER_NOT_WINDOWS_SAFE = 'device log writer is not yet Windows-safe (#1218)';
export const WINDOWS_SKIPPED: Record<string, string> = {
  'device_logs.spec.ts': LOG_WRITER_NOT_WINDOWS_SAFE,
  'logs_flicker.spec.ts': LOG_WRITER_NOT_WINDOWS_SAFE,
};

// skipOnWindows is called at the top of a spec file with its __filename.
export function skipOnWindows(test: typeof PlaywrightTest, specFile: string): void {
  const reason = WINDOWS_SKIPPED[basename(specFile)];
  test.skip(process.platform === 'win32' && reason !== undefined, reason ?? '');
}
