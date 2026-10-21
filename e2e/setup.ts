import { execFileSync, spawn, ChildProcess } from 'child_process';
import { existsSync, mkdtempSync } from 'fs';
import { join } from 'path';
import { tmpdir } from 'os';
import { ADMIN_KEY } from './adminKey';

let serverProcess: ChildProcess | null = null;
let adminUrl: string = '';
let certDir: string = '';
let protocolPort: number = 0;

// Admin and protocol listeners draw from adjacent, non-overlapping ranges.
const ADMIN_PORT_BASE = 18443;
const PROTOCOL_PORT_BASE = 19443;
const PORT_SPAN = 1000;

export type PortKind = 'admin' | 'protocol';
export type PortPicker = (kind: PortKind) => number;

// A random port in [base, base + span), never one of exclude.
export function randomPortIn(base: number, span: number, exclude: number[] = []): number {
  for (;;) {
    const port = base + Math.floor(Math.random() * span);
    if (!exclude.includes(port)) return port;
  }
}

const randomPort: PortPicker = (kind) =>
  randomPortIn(kind === 'admin' ? ADMIN_PORT_BASE : PROTOCOL_PORT_BASE, PORT_SPAN);

export function getAdminUrl(): string {
  return adminUrl;
}

// The protocol port of the running server; a spec reads it here rather than
// choosing its own, so a retry on fresh ports cannot leave it stale.
export function getProtocolPort(): number {
  return protocolPort;
}

export function getCertDir(): string {
  return certDir;
}

// extraEnv is merged over the harness defaults below, so a spec can boot
// the server in a non-default posture without duplicating the cert and port
// setup. It must not set SEP2_ADDR or SEP2_ADMIN_ADDR: the harness owns the
// ports and re-picks both on a retry. pickPort exists so a test can force a
// collision.
export async function startServer(
  extraEnv: Record<string, string> = {},
  pickPort: PortPicker = randomPort,
): Promise<string> {
  certDir = mkdtempSync(join(tmpdir(), 'sep2-e2e-'));
  const projectRoot = join(__dirname, '..');
  const sep2server = join(projectRoot, 'sep2server');

  // #209: the binary is built once in globalSetup (e2e/global-setup.ts)
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

  // Random ports can collide with another worker's server; the server then
  // exits on bind, so a start that dies early is retried on fresh ports.
  const attempts = 3;
  for (let attempt = 1; ; attempt++) {
    const adminPort = pickPort('admin');
    protocolPort = pickPort('protocol');
    adminUrl = `https://localhost:${adminPort}`;

    serverProcess = spawn(sep2server, ['serve'], {
      env: {
        ...process.env,
        SEP2_ADDR: `:${protocolPort}`,
        SEP2_CERT: join(certDir, 'server.crt'),
        SEP2_KEY: join(certDir, 'server.key'),
        SEP2_CA: join(certDir, 'ca.crt'),
        SEP2_CA_KEY: join(certDir, 'ca.key'),
        SEP2_ADMIN_ADDR: `:${adminPort}`,
        SEP2_ADMIN_KEY: ADMIN_KEY,
        // #161: admin listener defaults to plain HTTP. Force HTTPS with
        // self-signed cert so the e2e dashboard URL keeps the `https://...`
        // shape it used pre-split.
        SEP2_ADMIN_TLS: 'true',
        ...extraEnv,
      },
      stdio: ['pipe', 'pipe', 'pipe'],
    });

    try {
      // ResolveAdminBind logs a bare ":port" as "127.0.0.1:port".
      await waitForReady(serverProcess, new RegExp(`Admin server listening on \\S*:${adminPort} `), 20000);
      return adminUrl;
    } catch (err) {
      stopServer();
      if (!(err instanceof ServerExitedError) || attempt >= attempts) throw err;
    }
  }
}

export function stopServer() {
  if (serverProcess) {
    serverProcess.kill('SIGTERM');
    serverProcess = null;
  }
}

class ServerExitedError extends Error {}

// Resolves when the server logs that its admin listener is up (it logs to
// stderr), rejects if the process exits first or the log line never comes.
// Both pipes stay drained for the process's life so a full pipe cannot stall it.
function waitForReady(proc: ChildProcess, readyLine: RegExp, timeoutMs: number): Promise<void> {
  return new Promise((resolve, reject) => {
    let output = '';
    let ready = false;
    const timer = setTimeout(() => {
      reject(new Error(`Server did not log ${readyLine} within ${timeoutMs}ms. Output:\n${output}`));
    }, timeoutMs);
    const onData = (chunk: Buffer) => {
      if (ready) return;
      output += chunk.toString();
      if (readyLine.test(output)) {
        ready = true;
        clearTimeout(timer);
        resolve();
      }
    };
    proc.stdout?.on('data', onData);
    proc.stderr?.on('data', onData);
    proc.once('error', (err) => {
      clearTimeout(timer);
      reject(new Error(`Server failed to start: ${err.message}`));
    });
    proc.once('exit', (code, signal) => {
      clearTimeout(timer);
      reject(new ServerExitedError(`Server exited (code ${code}, signal ${signal}) before ready. Output:\n${output}`));
    });
  });
}
