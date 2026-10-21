// Mints device certificates for e2e specs. The one place `certs generate-device`
// is run, so a flag change is made once.
import { execFileSync } from 'child_process';
import { readFileSync } from 'fs';
import { join } from 'path';

export interface DeviceCert {
  name: string;
  certPEM: string;
  keyPEM: string;
}

// mintDeviceCert writes <name>.crt and <name>.key into certDir, signed by the
// CA the server under test was started with.
export function mintDeviceCert(
  serverBinary: string,
  certDir: string,
  name: string,
  hwSerial: string = `${name}-serial`,
): DeviceCert {
  execFileSync(serverBinary, [
    'certs', 'generate-device',
    '--ca', join(certDir, 'ca.crt'),
    '--ca-key', join(certDir, 'ca.key'),
    '--hw-serial', hwSerial,
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
