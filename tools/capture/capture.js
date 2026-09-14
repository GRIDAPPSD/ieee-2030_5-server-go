// Admin UI screenshot capture script (not shipped as part of the server).
// Usage: start the server on 127.0.0.1:38080; set CAPTURE_CHROME_PATH to a
// Playwright Chromium executable; provision the device cert this script
// reads with `make certs CERT_DIR=testrun/certs` then `make new-device
// DEVICE_NAME=parse-demo-device SERIAL=<serial> CERT_DIR=testrun/certs`;
// set CAPTURE_NO_SANDBOX=1 to add --no-sandbox (sandboxless containers only).
'use strict';

const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright-core');

const BASE = 'http://127.0.0.1:38080';
const IMG_DIR = path.join(__dirname, '..', '..', 'docs', 'images');
const CHROME_PATH = process.env.CAPTURE_CHROME_PATH;
if (!CHROME_PATH) {
  console.error('CAPTURE_CHROME_PATH is not set: point it at a Playwright chromium executable.');
  process.exit(1);
}
const NO_SANDBOX = process.env.CAPTURE_NO_SANDBOX === '1';
const DEVICE_PEM_PATH = path.join(
  __dirname, '..', '..', 'testrun', 'certs', 'parse-demo-device.crt',
);

const log = (...args) => console.log(new Date().toISOString(), ...args);

function outPath(name) {
  return path.join(IMG_DIR, name);
}

async function shootCard(page, h2Text, filename, opts = {}) {
  const card = page.locator('.card', { has: page.locator('h2', { hasText: h2Text }) }).first();
  await card.waitFor({ state: 'visible' });
  await card.screenshot({ path: outPath(filename), ...opts });
  log('captured', filename);
}

async function main() {
  const browser = await chromium.launch({
    executablePath: CHROME_PATH,
    args: NO_SANDBOX ? ['--no-sandbox'] : [],
  });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();

  const report = {};

  // 1. Load the dashboard, wait for the initial fetch to resolve.
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.waitForFunction(() => {
    const el = document.querySelector('#deviceCount');
    return el && el.textContent !== '' ;
  });
  await page.waitForTimeout(500); // let the SSE connect settle
  report.navBarDeviceCountAtLoad = await page.locator('#deviceCount').innerText();
  report.mupCountAtLoad = await page.locator('#mupCount').innerText();
  log('device count at load:', report.navBarDeviceCountAtLoad, 'mup count:', report.mupCountAtLoad);

  // Baseline / control full-page shot before any FSA/second-device seeding,
  // so the fully-seeded shot later can be told apart from this one.
  await page.screenshot({ path: outPath('admin-ui-full-page-overview-initial.png'), fullPage: true });
  log('captured admin-ui-full-page-overview-initial.png (control: pre-FSA, 1 device)');

  // 2. NavBar / Overview / ServerInfo.
  await page.locator('nav.navbar').screenshot({ path: outPath('admin-ui-navbar.png') });
  log('captured admin-ui-navbar.png');
  await shootCard(page, 'Connected Devices', 'admin-ui-connected-devices.png');
  await shootCard(page, 'Server Info', 'admin-ui-server-info.png');

  // 3. Certificate Management: blank, then post-generate.
  await shootCard(page, 'Certificate Management', 'admin-ui-certificate-management-blank.png');
  await page.fill('#hwSerial', 'CAPTURE-DEMO-CERT-01');
  await page.fill('#hwType', '1.3.6.1.4.1.40732.99');
  await page.click('button:has-text("Generate Device Cert")');
  await page.waitForFunction(() => {
    const el = document.querySelector('#certResult');
    return el && el.textContent.startsWith('Generated');
  });
  report.certGenerateResult = await page.locator('#certResult').innerText();
  log('cert generate result:', report.certGenerateResult);
  // Safety check before the screenshot lands: confirm no PEM/key material is
  // rendered anywhere in this card's visible text.
  const certCardText = await page
    .locator('.card', { has: page.locator('h2', { hasText: 'Certificate Management' }) })
    .first()
    .innerText();
  report.certCardLeakCheck = /BEGIN (CERTIFICATE|.*PRIVATE KEY)/.test(certCardText)
    ? 'FAIL: PEM material found in DOM text'
    : 'PASS: no PEM/key material in visible text';
  log('cert panel leak check:', report.certCardLeakCheck);
  if (report.certCardLeakCheck.startsWith('FAIL')) {
    throw new Error('Refusing to save certificate-management screenshot: ' + report.certCardLeakCheck);
  }
  await shootCard(page, 'Certificate Management', 'admin-ui-certificate-management-generated.png');

  // 4. Send DER Control (stub).
  await page.click('button:has-text("Send")');
  await page.waitForFunction(() => {
    const el = document.querySelector('#controlResult');
    return el && el.textContent !== '';
  });
  report.derControlResult = await page.locator('#controlResult').innerText();
  log('DER control stub result (sends nothing):', report.derControlResult);
  await shootCard(page, 'Send DER Control', 'admin-ui-send-der-control.png');

  // 5. Add End Device: blank, then parsed.
  await shootCard(page, 'Add End Device', 'admin-ui-add-end-device-blank.png');
  const devicePem = fs.readFileSync(DEVICE_PEM_PATH, 'utf8');
  await page.fill('#addDevCert', devicePem);
  await page.click('button:has-text("Parse Cert")');
  await page.waitForFunction(() => {
    const el = document.querySelector('#addDevResult');
    return el && el.textContent.startsWith('Parsed cert');
  });
  const parsedSFDI = await page.locator('#addDevSFDI').inputValue();
  const parsedLFDI = await page.locator('#addDevLFDI').inputValue();
  report.parsedSFDI = parsedSFDI;
  report.parsedLFDI = parsedLFDI;
  log('parsed SFDI/LFDI:', parsedSFDI, parsedLFDI);
  await shootCard(page, 'Add End Device', 'admin-ui-add-end-device-parsed.png');

  // Complete the add so this device is real for the Lookup "found" case
  // and for the End Devices table / FSA assignment steps below.
  await page.fill('#addDevDesc', 'Capture demo device');
  await page.fill('#addDevPIN', '135790');
  await page.click('button:has-text("Add Device")');
  await page.waitForFunction(() => {
    const el = document.querySelector('#addDevResult');
    return el && el.textContent.startsWith('Created');
  });
  report.addDeviceResult = await page.locator('#addDevResult').innerText();
  log('add device result:', report.addDeviceResult);

  // 6. Lookup Device: found, then missing.
  await page.fill('#lookupLFDI', parsedLFDI);
  await page.click('button:has-text("Lookup")');
  await page.waitForFunction(() => {
    const el = document.querySelector('#lookupResult');
    return el && el.textContent.startsWith('Found:');
  });
  report.lookupFoundResult = await page.locator('#lookupResult').innerText();
  log('lookup found result:', report.lookupFoundResult);
  await shootCard(page, 'Lookup Device by LFDI', 'admin-ui-lookup-device-found.png');

  const absentLFDI = 'A'.repeat(40);
  await page.fill('#lookupLFDI', absentLFDI);
  await page.click('button:has-text("Lookup")');
  await page.waitForFunction(() => {
    const el = document.querySelector('#lookupResult');
    return el && el.textContent.includes('No device registered');
  });
  report.lookupMissingResult = await page.locator('#lookupResult').innerText();
  log('lookup missing result:', report.lookupMissingResult);
  await shootCard(page, 'Lookup Device by LFDI', 'admin-ui-lookup-device-missing.png');

  // 7. Create FSA Template.
  await page.fill('#newFSADesc', 'Capture Demo FSA');
  await page.click('button:has-text("Create FSA")');
  await page.waitForFunction(() => {
    const el = document.querySelector('#createFSAResult');
    return el && el.textContent.startsWith('Created');
  });
  report.createFsaResult = await page.locator('#createFSAResult').innerText();
  log('create FSA result:', report.createFsaResult);
  await shootCard(page, 'Create FSA Template', 'admin-ui-create-fsa-template.png');

  const mridMatch = report.createFsaResult.match(/\(mRID=([^)]+)\)/);
  const fsaMRID = mridMatch ? mridMatch[1] : null;
  report.fsaMRID = fsaMRID;
  log('created FSA mRID:', fsaMRID);
  if (!fsaMRID) {
    throw new Error('Could not parse FSA mRID out of: ' + report.createFsaResult);
  }

  // 8. Attach a program to the FSA (via the topology tree's inline FsaNode
  // form for that FSA at the SY "templates" level -- it is unassigned so
  // far, which is exactly where FsaNode renders it).
  await page.waitForSelector(`#attachInp-${fsaMRID}`, { timeout: 10000 });
  await page.fill(`#attachInp-${fsaMRID}`, `/edev/seed-dev-1/fsa/seed-fsa-1/derp/derp-1`);
  const attachRow = page.locator(`#attachInp-${fsaMRID}`).locator('..');
  await attachRow.locator('button', { hasText: 'Attach program' }).click();
  await page.waitForFunction((mrid) => {
    const el = document.querySelector(`#attachResult-${mrid}`);
    return el && el.textContent.startsWith('Attached');
  }, fsaMRID);
  report.attachProgramResult = await page.locator(`#attachResult-${fsaMRID}`).innerText();
  log('attach program result:', report.attachProgramResult);

  // 9. Assign the device (the just-added one) to the FSA via DeviceTable.
  // deviceID in the DeviceTable is the trailing href segment; the
  // just-added device's href came back in addDeviceResult ("Created
  // /edev/<id> ...").
  const hrefMatch = report.addDeviceResult.match(/Created (\/edev\/[^\s]+)/);
  if (!hrefMatch) {
    throw new Error('Could not parse device href out of: ' + report.addDeviceResult);
  }
  const deviceHref = hrefMatch[1];
  const devId = deviceHref.substring(deviceHref.lastIndexOf('/') + 1);
  report.deviceHref = deviceHref;
  report.devId = devId;
  log('device href/id for assignment:', deviceHref, devId);

  await page.waitForSelector(`#assignSel-${devId}`, { timeout: 10000 });
  await page.selectOption(`#assignSel-${devId}`, `/api/fsas/${fsaMRID}`);
  const assignCell = page.locator(`#assignSel-${devId}`).locator('..');
  await assignCell.locator('button', { hasText: 'Assign' }).click();
  // No fixed result element for assign success; wait for the FSA catalog to
  // reflect the assignment instead (poll via a fresh /api/fsas fetch proxy:
  // just wait for the DOM to show the assigned device under this FSA).
  await page.waitForFunction(({ mrid, dev }) => {
    const rows = Array.from(document.querySelectorAll('[data-testid="fsa-catalog-mrid"]'));
    const row = rows.find((r) => r.textContent === mrid);
    if (!row) return false;
    const tr = row.closest('tr');
    return tr && tr.textContent.includes(dev);
  }, { mrid: fsaMRID, dev: devId }, { timeout: 10000 });
  log('device assignment reflected in FSA catalog');

  // 10. FSA Templates catalog (now non-empty, with a program and an
  // assigned device + Unassign button).
  await shootCard(page, 'FSA Templates', 'admin-ui-fsa-templates-catalog.png');

  // 11. FSA Tree (SY -> FD -> SP -> DEV), device+FSA+program all attached,
  // default-expanded.
  await shootCard(page, 'FSA Tree', 'admin-ui-fsa-tree.png');

  // 12. End Devices table (2 devices now: the boot-fixture seed device and
  // the one just added live).
  await shootCard(page, 'End Devices', 'admin-ui-end-devices-table.png');

  // 13. Device Activity chart: capture immediately (predicted: near-empty),
  // then wait through several SSE ticks and capture again.
  await shootCard(page, 'Device Activity', 'admin-ui-device-activity-early.png');
  const waitStart = Date.now();
  const scratchDir = path.join(__dirname, 'scratch');
  // Poll intermediate states into scratch/ (not docs/images -- these are
  // for my own observation of the growth curve, not deliverables) so I
  // can pick the point where the line is legible rather than guessing.
  const card = page.locator('.card', { has: page.locator('h2', { hasText: 'Device Activity' }) }).first();
  for (let i = 0; i < 8; i++) {
    await page.waitForTimeout(5000);
    const elapsedS = Math.round((Date.now() - waitStart) / 1000);
    await card.screenshot({ path: path.join(scratchDir, `activity-tick-${i + 1}-${elapsedS}s.png`) });
    log(`activity chart wait: ${elapsedS}s elapsed (tick ${i + 1}), scratch capture saved`);
  }
  report.activityChartWaitSeconds = Math.round((Date.now() - waitStart) / 1000);
  await shootCard(page, 'Device Activity', 'admin-ui-device-activity-legible.png');
  log('captured device activity after', report.activityChartWaitSeconds, 'seconds of waiting');

  // 14. Full-page overview, fully seeded (final canonical shot).
  await page.screenshot({ path: outPath('admin-ui-full-page-overview.png'), fullPage: true });
  log('captured admin-ui-full-page-overview.png (final, fully seeded)');

  // 15. Admin Login, state (a): standalone /login page, separate tab.
  const loginPage = await context.newPage();
  await loginPage.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await loginPage.screenshot({ path: outPath('admin-ui-admin-login.png'), fullPage: true });
  log('captured admin-ui-admin-login.png');
  await loginPage.close();

  // 16. Admin Login, state (b) diagnostic: clear cookies and reload the
  // dashboard tab to see whether the SPA-embedded LoginPanel appears.
  // Prediction: it will not, because the loopback bypass admits the
  // request before any cookie is consulted, so no 401 is ever produced
  // for the SPA's mount-time probe to react to.
  await context.clearCookies();
  await page.reload({ waitUntil: 'networkidle' });
  const loginPanelVisible = await page.locator('[data-testid="login-panel"]').count();
  report.loginPanelAfterCookieClear = loginPanelVisible > 0 ? 'VISIBLE' : 'NOT VISIBLE';
  log('SPA-embedded LoginPanel after cookie clear + reload:', report.loginPanelAfterCookieClear);

  fs.writeFileSync(path.join(__dirname, 'capture-report.json'), JSON.stringify(report, null, 2));
  log('report written to capture-report.json');

  await browser.close();
}

main().catch((err) => {
  console.error('CAPTURE FAILED:', err);
  process.exit(1);
});
