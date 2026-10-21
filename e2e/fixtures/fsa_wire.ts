// Reads a list from the protocol port over mutual TLS, the way a device does.
import { readFileSync } from 'fs';
import { request } from 'https';
import { join } from 'path';
import type { DeviceCert } from './device_cert';

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
