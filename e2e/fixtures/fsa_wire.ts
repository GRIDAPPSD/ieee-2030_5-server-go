// Talks to the protocol port over mutual TLS, the way a device does.
import { readFileSync } from 'fs';
import { request } from 'https';
import { join } from 'path';
import type { DeviceCert } from './device_cert';

export interface WireReply {
  status: number;
  body: string;
  location: string;
}

// sendWire requests path on the protocol port as the device, over the CCM-8
// suite IEEE 2030.5 requires. The server's certificate is checked against
// the test CA. A body is sent as application/sep+xml.
export function sendWire(
  port: number,
  certDir: string,
  device: DeviceCert,
  method: string,
  path: string,
  body?: string,
): Promise<WireReply> {
  return new Promise((resolve, reject) => {
    const headers: Record<string, string | number> = { Accept: 'application/sep+xml' };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/sep+xml';
      headers['Content-Length'] = Buffer.byteLength(body);
    }
    const req = request(
      {
        host: 'localhost',
        port,
        path,
        method,
        cert: device.certPEM,
        key: device.keyPEM,
        ca: readFileSync(join(certDir, 'ca.crt')),
        ciphers: 'ECDHE-ECDSA-AES128-CCM8:@SECLEVEL=0',
        minVersion: 'TLSv1.2',
        maxVersion: 'TLSv1.2',
        headers,
      },
      (res) => {
        let text = '';
        res.setEncoding('utf8');
        res.on('data', (c: string) => (text += c));
        res.on('end', () =>
          resolve({ status: res.statusCode ?? 0, body: text, location: String(res.headers.location ?? '') }),
        );
      },
    );
    req.setTimeout(5000, () => req.destroy(new Error(`wire ${method} of ${path} timed out`)));
    req.on('error', reject);
    req.end(body);
  });
}

// readWire GETs path as the device.
export async function readWire(
  port: number,
  certDir: string,
  device: DeviceCert,
  path: string,
): Promise<{ status: number; body: string }> {
  const { status, body } = await sendWire(port, certDir, device, 'GET', path);
  return { status, body };
}
