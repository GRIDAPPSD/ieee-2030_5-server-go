// Helpers for e2e specs that act as a device: mint a device certificate and
// read a list from the protocol port over mutual TLS, the way a device does.
import { execFileSync } from 'child_process';
import { readFileSync } from 'fs';
import { request } from 'https';
import { join } from 'path';

export interface DeviceCert {
  name: string;
  certPEM: string;
  keyPEM: string;
}

// mintDeviceCert writes <name>.crt and <name>.key into certDir, signed by the
// CA the server under test was started with.
export function mintDeviceCert(serverBinary: string, certDir: string, name: string): DeviceCert {
  execFileSync(serverBinary, [
    'certs', 'generate-device',
    '--ca', join(certDir, 'ca.crt'),
    '--ca-key', join(certDir, 'ca.key'),
    '--hw-serial', `${name}-serial`,
    '--hw-type', '1.3.6.1.4.1.40732.99',
    '--out', certDir,
    '--name', name,
  ], { stdio: 'pipe' });
  return {
    name,
    certPEM: readFileSync(join(certDir, `${name}.crt`), 'utf8'),
    keyPEM: readFileSync(join(certDir, `${name}.key`), 'utf8'),
  };
}

// readWire GETs path on the protocol port as the device, over the CCM-8
// suite IEEE 2030.5 requires. The server's certificate is checked against
// the test CA.
export function readWire(
  port: number,
  certDir: string,
  device: DeviceCert,
  path: string,
): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const req = request(
      {
        host: 'localhost',
        port,
        path,
        method: 'GET',
        cert: device.certPEM,
        key: device.keyPEM,
        ca: readFileSync(join(certDir, 'ca.crt')),
        ciphers: 'ECDHE-ECDSA-AES128-CCM8:@SECLEVEL=0',
        minVersion: 'TLSv1.2',
        maxVersion: 'TLSv1.2',
        headers: { Accept: 'application/sep+xml' },
      },
      (res) => {
        let body = '';
        res.setEncoding('utf8');
        res.on('data', (c: string) => (body += c));
        res.on('end', () => resolve({ status: res.statusCode ?? 0, body }));
      },
    );
    req.setTimeout(5000, () => req.destroy(new Error(`wire read of ${path} timed out`)));
    req.on('error', reject);
    req.end();
  });
}
