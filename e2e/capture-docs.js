const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { chromium, expect } = require('@playwright/test');

const root = path.resolve(__dirname, '..');
const apiKey = 'emberdeck-e2e-test-key';
const screenshotDir = path.join(root, 'docs/assets/screenshots');

function portAvailable() {
  return new Promise((resolve, reject) => {
    const listener = net.createServer();
    listener.once('error', reject);
    listener.listen(0, '127.0.0.1', () => {
      const { port } = listener.address();
      listener.close((error) => error ? reject(error) : resolve(port));
    });
  });
}

async function waitForServer(url, child) {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`EmberDeck exited early (${child.exitCode})`);
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch (_) { /* The listener is not ready yet. */ }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`EmberDeck did not start at ${url}`);
}

async function stopServer(child) {
  if (!child || child.exitCode !== null) return;
  const exited = new Promise((resolve) => child.once('exit', resolve));
  child.kill('SIGTERM');
  const timeout = setTimeout(() => child.kill('SIGKILL'), 10000);
  try { await exited; } finally { clearTimeout(timeout); }
}

async function capture(page, name) {
  await page.screenshot({ path: path.join(screenshotDir, name), fullPage: true });
}

async function main() {
  const binary = path.resolve(process.env.EMBERDECK_BINARY || path.join(root, 'bin', process.platform === 'win32' ? 'emberdeck.exe' : 'emberdeck'));
  if (!process.env.EMBERDECK_BINARY) {
    fs.mkdirSync(path.dirname(binary), { recursive: true });
    const result = spawnSync('go', ['build', '-o', binary, './cmd/emberdeck'], { cwd: root, stdio: 'inherit' });
    if (result.status !== 0) throw new Error('Could not build EmberDeck for screenshot capture');
  }
  const port = await portAvailable();
  const baseURL = `http://127.0.0.1:${port}`;
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'emberdeck-guide-capture-'));
  const server = spawn(binary, ['serve', '-listen', `127.0.0.1:${port}`, '-data', dataDir, '-simulate'], {
    cwd: root,
    stdio: 'inherit',
    env: { ...process.env, EMBERDECK_API_KEY: apiKey },
  });
  let browser;
  try {
    await waitForServer(baseURL, server);
    browser = await chromium.launch();
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' });
    const page = await context.newPage();
    await page.goto(baseURL);
    await page.locator('#api-key').fill(apiKey);
    await page.getByRole('button', { name: /Connect/ }).click();
    await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
    await expect(page.locator('#driver-select')).toHaveValue('simulator');

    const lines = 4000;
    const name = 'documented-controller-demo.gcode';
    const program = `${Array.from({ length: lines }, (_, i) => `G1 X${i % 100} F100`).join('\n')}\n`;
    await page.locator('#job-file').setInputFiles({ name, mimeType: 'text/plain', buffer: Buffer.from(program) });
    await page.locator('#job-name').fill(name);
    await page.getByRole('button', { name: /Add to library/ }).click();
    const row = page.locator('#job-list li').filter({ hasText: name });
    await expect(row).toContainText(/queued/i);
    await row.getByRole('button', { name: 'View' }).click();
    await page.locator('#close-program').click();
    await capture(page, 'operator-console-library.png');

    await page.locator('#operator-confirmation').check();
    await row.getByRole('button', { name: 'Start' }).click();
    const progress = row.locator('progress');
    await expect.poll(() => progress.evaluate((node) => node.value), { timeout: 15000 }).toBeGreaterThan(0);
    await row.getByRole('button', { name: 'View' }).click();
    await expect(page.locator('#program-panel')).toBeVisible();
    await capture(page, 'operator-console-running-view.png');

    await row.getByRole('button', { name: 'Pause' }).click();
    await expect(row).toContainText(/paused/i, { timeout: 15000 });
    await expect(row).toContainText('Last controller report: Hold:0');
    const holdReports = [];
    await expect.poll(async () => {
      const jobs = await page.evaluate(async () => {
        const response = await fetch('/api/v1/jobs', { headers: { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}` } });
        return response.json();
      });
      const job = jobs.find((candidate) => candidate.name === name);
      if (job) holdReports.push(job.controller_state);
      return holdReports.filter((state) => state === 'Hold:0').length;
    }, { timeout: 5000, intervals: [250] }).toBeGreaterThanOrEqual(2);
    await row.getByRole('button', { name: 'View' }).click();
    await capture(page, 'operator-console-paused.png');

    await page.setViewportSize({ width: 390, height: 844 });
    await capture(page, 'operator-console-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1000 });

    await page.locator('#operator-confirmation').check();
    await row.getByRole('button', { name: 'Resume' }).click();
    await expect(page.locator('#operator-confirmation')).not.toBeChecked();
    await expect(row).toContainText(/completed/i, { timeout: 120000 });
    await row.getByRole('button', { name: 'View' }).click();
    await capture(page, 'operator-console-completed.png');

    page.once('dialog', (dialog) => dialog.accept());
    await row.getByRole('button', { name: 'Delete' }).click();
    await expect(page.locator('#job-list li')).toHaveCount(0);
    const canceledName = 'documented-paused-cancel.gcode';
    const canceledProgram = `${Array.from({ length: 1200 }, () => 'G1 X1 F100').join('\n')}\n`;
    await page.locator('#job-file').setInputFiles({ name: canceledName, mimeType: 'text/plain', buffer: Buffer.from(canceledProgram) });
    await page.locator('#job-name').fill(canceledName);
    await page.getByRole('button', { name: /Add to library/ }).click();
    const canceledRow = page.locator('#job-list li').filter({ hasText: canceledName });
    await expect(canceledRow).toContainText(/queued/i);
    await page.locator('#operator-confirmation').check();
    await canceledRow.getByRole('button', { name: 'Start' }).click();
    const canceledProgress = canceledRow.locator('progress');
    await expect.poll(() => canceledProgress.evaluate((node) => node.value), { timeout: 15000 }).toBeGreaterThan(0);
    await canceledRow.getByRole('button', { name: 'Pause' }).click();
    await expect(canceledRow).toContainText(/paused/i, { timeout: 15000 });
    await expect(canceledRow).toContainText('Last controller report: Hold:0');
    await canceledRow.getByRole('button', { name: 'Cancel' }).click();
    await expect(canceledRow).toContainText(/canceled/i, { timeout: 10000 });
    await expect(canceledRow.getByRole('button', { name: 'Delete' })).toBeVisible();
    await canceledRow.getByRole('button', { name: 'View' }).click();
    await capture(page, 'operator-console-canceled.png');
    await context.close();
  } finally {
    if (browser) await browser.close();
    await stopServer(server);
    fs.rmSync(dataDir, { recursive: true, force: true });
  }
}

fs.mkdirSync(screenshotDir, { recursive: true });
main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
