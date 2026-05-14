import { execFileSync, spawn, ChildProcess } from 'child_process';
import { existsSync, mkdtempSync } from 'fs';
import { join } from 'path';
import { tmpdir } from 'os';

let serverProcess: ChildProcess | null = null;
let adminUrl: string = '';
let certDir: string = '';

export function getAdminUrl(): string {
  return adminUrl;
}

export function getCertDir(): string {
  return certDir;
}

export async function startServer(): Promise<string> {
  certDir = mkdtempSync(join(tmpdir(), 'sep2-e2e-'));
  const projectRoot = join(__dirname, '..');
  const sep2server = join(projectRoot, 'sep2server');

  // IEEE-114: the binary is built once in globalSetup (e2e/global-setup.ts)
  // before any worker starts, eliminating the ETXTBSY race that occurred when
  // multiple workers raced to `go build -o sep2server` against the same path.
  // Fail fast here so a misconfigured run surfaces immediately instead of
  // failing with a confusing exec error.
  if (!existsSync(sep2server)) {
    throw new Error(
      `sep2server binary not found at ${sep2server}. ` +
      'globalSetup (e2e/global-setup.ts) must run before workers start. ' +
      'Check that playwright.config.ts has globalSetup configured.'
    );
  }

  // Generate certs
  execFileSync(sep2server, ['certs', 'generate-ca', '--out', certDir], { stdio: 'pipe' });
  execFileSync(sep2server, [
    'certs', 'generate-server',
    '--ca', join(certDir, 'ca.crt'),
    '--ca-key', join(certDir, 'ca.key'),
    '--hosts', 'localhost,127.0.0.1',
    '--out', certDir,
  ], { stdio: 'pipe' });

  // Pick random ports
  const adminPort = 18443 + Math.floor(Math.random() * 1000);
  const protocolPort = 19443 + Math.floor(Math.random() * 1000);
  adminUrl = `https://localhost:${adminPort}`;

  // Start server
  serverProcess = spawn(sep2server, ['serve'], {
    env: {
      ...process.env,
      SEP2_ADDR: `:${protocolPort}`,
      SEP2_CERT: join(certDir, 'server.crt'),
      SEP2_KEY: join(certDir, 'server.key'),
      SEP2_CA: join(certDir, 'ca.crt'),
      SEP2_CA_KEY: join(certDir, 'ca.key'),
      SEP2_ADMIN_ADDR: `:${adminPort}`,
      SEP2_ADMIN_KEY: 'e2e-test-key',
      // IEEE-094: admin listener defaults to plain HTTP. Force HTTPS with
      // self-signed cert so the e2e dashboard URL keeps the `https://...`
      // shape it used pre-split.
      SEP2_ADMIN_TLS: 'true',
    },
    stdio: ['pipe', 'pipe', 'pipe'],
  });

  await waitForServer(adminUrl, 10000);
  return adminUrl;
}

export function stopServer() {
  if (serverProcess) {
    serverProcess.kill('SIGTERM');
    serverProcess = null;
  }
}

async function waitForServer(url: string, timeoutMs: number): Promise<void> {
  const start = Date.now();
  // Node 18+ has global fetch but self-signed certs need special handling
  // Use a simple TCP connect check instead
  const { connect } = await import('net');
  const urlObj = new URL(url);
  const port = parseInt(urlObj.port);

  while (Date.now() - start < timeoutMs) {
    const connected = await new Promise<boolean>((resolve) => {
      const socket = connect(port, 'localhost', () => {
        socket.destroy();
        resolve(true);
      });
      socket.on('error', () => {
        socket.destroy();
        resolve(false);
      });
      socket.setTimeout(500, () => {
        socket.destroy();
        resolve(false);
      });
    });
    if (connected) return;
    await new Promise(r => setTimeout(r, 200));
  }
  throw new Error(`Server did not start within ${timeoutMs}ms`);
}
