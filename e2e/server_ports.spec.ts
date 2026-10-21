import { test, expect } from '@playwright/test';
import { createServer, type Server } from 'net';
import { startServer, stopServer, getAdminUrl, getProtocolPort, type PortKind } from './setup';

// A start whose first ports are taken must come up on fresh ones: the server
// exits on a bind failure and the harness retries with new ports.

function occupy(port: number, host: string): Promise<Server> {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.once('error', reject);
    s.listen(port, host, () => resolve(s));
  });
}

function close(s: Server): Promise<void> {
  return new Promise((resolve) => s.close(() => resolve()));
}

const free = () => 24000 + Math.floor(Math.random() * 3000);

for (const taken of ['admin', 'protocol'] as PortKind[]) {
  test(`a taken ${taken} port is replaced on retry`, async () => {
    const busy = free();
    const blocker = await occupy(busy, taken === 'admin' ? '127.0.0.1' : '::');
    const picked: Record<PortKind, number[]> = { admin: [], protocol: [] };
    try {
      await startServer({ SEP2_PEN: '12345' }, (kind) => {
        const first = picked[kind].length === 0;
        const port = first && kind === taken ? busy : free() + (kind === 'admin' ? 0 : 3000);
        picked[kind].push(port);
        return port;
      });
      expect(picked[taken].length).toBeGreaterThan(1);
      expect(picked.admin.length).toBe(picked.protocol.length);
      expect(new URL(getAdminUrl()).port).toBe(String(picked.admin.at(-1)));
      expect(getProtocolPort()).toBe(picked.protocol.at(-1));
      expect(taken === 'admin' ? new URL(getAdminUrl()).port : String(getProtocolPort())).not.toBe(String(busy));
    } finally {
      stopServer();
      await close(blocker);
    }
  });
}
