import { execFileSync } from 'child_process';
import { join } from 'path';

/**
 * Playwright globalSetup — runs once before any worker starts.
 *
 * Builds the sep2server binary so workers can exec it without racing on
 * the output path. Each worker still creates its own certDir and spawns
 * its own server instance; only the build step is hoisted here.
 *
 * Fixes IEEE-114: concurrent workers calling `go build -o sep2server` to the
 * same shared path caused ETXTBSY on exec (one worker writing while another
 * exec'd the partial binary).
 */
export default function globalSetup(): void {
  const projectRoot = join(__dirname, '..');
  const sep2server = join(projectRoot, 'sep2server');

  execFileSync('go', ['build', '-o', sep2server, './cmd/sep2server/'], {
    cwd: projectRoot,
    stdio: 'pipe',
  });
}
