const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
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

async function captureElement(page, selector, name) {
  await page.locator(selector).screenshot({ path: path.join(screenshotDir, name), animations: 'disabled' });
}

async function captureDocumentationNavigation(browser) {
  const python = process.env.PYTHON || 'python3';
  const result = spawnSync(python, ['scripts/build-docs.py'], { cwd: root, stdio: 'inherit' });
  if (result.status !== 0) throw new Error('Could not render documentation for navigation screenshots');
  const guide = pathToFileURL(path.join(root, 'dist/docs/operator-guide.html')).href;

  for (const [name, width, height, isMobile] of [
    ['desktop', 1440, 1000, false],
    ['mobile', 390, 844, true],
  ]) {
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: 1, isMobile, hasTouch: isMobile });
    await page.goto(guide);
    await expect(page.getByRole('navigation', { name: 'Documentation' })).toBeVisible();
    await expect(page.locator('nav a[aria-current="page"]')).toHaveAttribute('href', 'operator-guide.html');
    const menu = page.locator('nav details').nth(isMobile ? 0 : 3);
    const summary = menu.locator('summary');
    if (isMobile) await summary.tap();
    else await summary.click();
    await expect(menu).toHaveAttribute('open', '');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: path.join(screenshotDir, `docs-navigation-after-${name}.png`) });
    await page.close();
  }
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

    await page.getByRole('button', { name: 'New profile' }).click();
    await page.getByLabel('Profile name').fill('Documented GRBL');
    await page.getByLabel('Work area width (mm)').fill('300');
    await page.getByLabel('Work area height (mm)').fill('200');
    await page.getByLabel('Maximum spindle value ($30)').fill('1000');
    await page.getByLabel('Minimum feed (mm/min)').fill('1');
    await page.getByLabel('Maximum feed (mm/min)').fill('6000');
    await page.getByLabel('Maximum commanded power (%)').fill('100');
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect(page.locator('#designer-status')).toContainText('Profile saved');
    await page.getByRole('button', { name: 'Edit profile' }).click();
    await page.locator('#profile-name').evaluate((input) => input.blur());
    await captureElement(page, '#designer-panel', 'designer-profile.png');
    await page.getByRole('button', { name: 'Cancel' }).click();

    await page.getByRole('button', { name: 'New rectangle project' }).click();
    await page.locator('#project-name').fill('Panel outline');
    await page.locator('#rect-x').fill('10');
    await page.locator('#rect-y').fill('20');
    await page.locator('#rect-width').fill('30');
    await page.locator('#rect-height').fill('20');
    await page.locator('#operation-feed').fill('600');
    await page.locator('#operation-power').fill('25');
    await expect(page.locator('#project-validation')).toBeHidden();
    await page.getByRole('button', { name: 'Save project' }).click();
    await expect(page.locator('#designer-status')).toContainText('Project saved');
    await captureElement(page, '#project-form', 'designer-project.png');
    await page.locator('#rect-width').fill('400');
    await expect(page.locator('#project-validation')).toContainText(/outside the declared workspace/i);
    await expect(page.getByRole('button', { name: 'Save project' })).toBeDisabled();
    await captureElement(page, '#project-form', 'designer-project-out-of-bounds.png');
    await page.locator('#rect-width').fill('30');
    await page.getByRole('button', { name: 'Save project' }).click();
    await expect(page.locator('#designer-status')).toContainText('Project saved');
    await page.getByRole('button', { name: 'Close editor' }).click();
    await expect(page.getByRole('button', { name: 'Reopen Panel outline' })).toBeVisible();
    await capture(page, 'designer-console-overview.png');
    await page.getByRole('button', { name: 'Reopen Panel outline' }).click();
    await page.getByRole('button', { name: 'Edit profile' }).click();
    await page.getByLabel('Work area width (mm)').fill('290');
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect(page.locator('#stale-profile-warning')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
    await capture(page, 'designer-stale-profile.png');
    await page.getByRole('button', { name: 'Save project' }).click();
    await expect(page.locator('#stale-profile-warning')).toBeHidden();
    await page.getByRole('button', { name: 'Generate queued job' }).click();
    const designerRow = page.locator('#job-list li').filter({ hasText: 'Panel outline.gcode' });
    await expect(designerRow).toContainText(/queued/i);
    await expect(page.locator('#program-provenance')).toContainText('Panel outline');
    await captureElement(page, '#program-panel', 'designer-generated-review.png');
    await page.setViewportSize({ width: 390, height: 844 });
    await captureElement(page, '#designer-panel', 'designer-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1000 });

    await page.getByRole('button', { name: 'New vector project' }).click();
    await page.locator('#vector-name').fill('Three shapes, two operations');
    await page.getByRole('button', { name: 'Add rectangle' }).click();
    await page.getByRole('button', { name: 'Add ellipse' }).click();
    await page.getByRole('button', { name: 'Add line' }).click();
    await page.getByRole('button', { name: 'Add layer' }).click();
    await page.getByRole('combobox', { name: 'Operation 2 type' }).selectOption('vector-cut');
    await page.getByLabel('Operation 2 feed (mm/min)').fill('300');
    await page.getByLabel('Operation 2 power (%)').fill('50');
    await page.getByRole('button', { name: '2. ellipse' }).click();
    await page.locator('#vector-object-operation').selectOption({ label: '2. cut' });
    await page.getByRole('button', { name: '3. line' }).click();
    await page.locator('#vector-object-operation').selectOption({ label: '1. engrave' });
    await expect(page.locator('#vector-validation')).toBeHidden();
    await page.getByRole('button', { name: 'Save vector project' }).click();
    await expect(page.locator('#designer-status')).toContainText('Vector project saved');
    await captureElement(page, '#vector-form', 'designer-vector-project.png');
    await page.locator('#generate-vector-project').click();
    const vectorRow = page.locator('#job-list li').filter({ hasText: 'Three shapes, two operations.gcode' });
    await expect(vectorRow).toContainText(/queued/i);
    await expect(page.locator('#program-provenance')).toContainText('2. Cut');
    await captureElement(page, '#program-panel', 'designer-vector-review.png');
    await page.setViewportSize({ width: 390, height: 844 });
    await captureElement(page, '#vector-form', 'designer-vector-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.locator('#close-program').click();
    await page.locator('#close-vector-project').click();
    await page.getByRole('button', { name: 'New vector project' }).click();
    await page.locator('#vector-name').fill('Imported SVG paths');
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="90mm" height="55mm" viewBox="0 0 90 55"><path d="M10 45 L35 45 L35 25 L10 25 Z"/><path d="M50 45 C58 25 72 25 80 45"/></svg>`;
    await page.locator('#vector-svg-file').setInputFiles({ name: 'imported-shapes.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(svg) });
    await expect(page.locator('#designer-status')).toContainText('Imported 2 editable SVG paths');
    await expect(page.locator('#vector-object-list li')).toHaveCount(2);
    await captureElement(page, '#vector-form', 'designer-svg-import.png');
    await page.setViewportSize({ width: 390, height: 844 });
    await captureElement(page, '#vector-form', 'designer-svg-import-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.locator('#close-vector-project').click();
    page.once('dialog', (dialog) => dialog.accept());
    await vectorRow.getByRole('button', { name: 'Delete' }).click();
    await expect(vectorRow).toHaveCount(0);
    const vectorProject = page.locator('#vector-project-list li').filter({ hasText: 'Three shapes, two operations' });
    page.once('dialog', (dialog) => dialog.accept());
    await vectorProject.getByRole('button', { name: 'Delete' }).click();
    await expect(vectorProject).toHaveCount(0);

    const projectRow = page.locator('#project-list li').filter({ hasText: 'Panel outline' });
    page.once('dialog', (dialog) => dialog.accept());
    await projectRow.getByRole('button', { name: 'Delete project Panel outline' }).click();
    await expect(projectRow).toHaveCount(0);
    page.once('dialog', (dialog) => dialog.accept());
    await designerRow.getByRole('button', { name: /Delete Panel outline\.gcode/ }).click();
    await expect(designerRow).toHaveCount(0);

    const lines = 1000;
    const name = 'documented-controller-demo.gcode';
    const program = `G21\nG90\nG0 X0 Y0\nM4 S100\n${Array.from({ length: lines }, (_, i) => `G1 X${i + 1} Y${Math.floor(i / 50)} F300`).join('\n')}\nM5\n`;
    await page.locator('#job-file').setInputFiles({ name, mimeType: 'text/plain', buffer: Buffer.from(program) });
    await page.locator('#job-name').fill(name);
    await page.getByRole('button', { name: /Add to library/ }).click();
    const row = page.locator('#job-list li').filter({ hasText: name });
    await expect(row).toContainText(/queued/i);
    await row.getByRole('button', { name: 'View', exact: true }).click();
    await page.locator('#close-program').click();
    await expect(row.locator('.toolpath-summary')).toContainText(/bounds out_of_bounds/i);
    await expect(row.locator('.toolpath-profile-bounds')).toHaveCount(1);
    await expect(row.getByRole('checkbox', { name: /Override out_of_bounds bounds review/ })).toBeVisible();
    await capture(page, 'operator-console-uploaded-bounds-review.png');
    await capture(page, 'operator-console-library.png');

    await page.locator('#operator-confirmation').check();
    const boundsOverride = row.getByRole('checkbox', { name: /Override .* bounds review/ });
    if (await boundsOverride.count()) await boundsOverride.check();
    await row.getByRole('button', { name: 'Start' }).click();
    const progress = row.locator('progress');
    await expect.poll(() => progress.evaluate((node) => node.value), { timeout: 15000 }).toBeGreaterThan(0);
    await row.getByRole('button', { name: 'View', exact: true }).click();
    await expect(page.locator('#program-panel')).toBeVisible();
    await expect(row.locator('.toolpath-live')).toBeVisible();
    await expect(row.locator('.toolpath-summary')).toContainText(/estimated/i);
    await capture(page, 'operator-console-running-view.png');

    await row.getByRole('button', { name: 'Pause' }).click();
    await expect(row).toContainText(/paused/i, { timeout: 15000 });
    await expect(row).toContainText('Last controller report: Hold:0');
    const holdReports = [];
    await expect.poll(async () => {
      const jobs = await page.evaluate(async (jobName) => {
        const response = await fetch(`/api/v1/jobs?name=${encodeURIComponent(jobName)}`, { headers: { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}` } });
        return (await response.json()).jobs;
      }, name);
      const job = jobs.find((candidate) => candidate.name === name);
      if (job) holdReports.push(job.controller_state);
      return holdReports.filter((state) => state === 'Hold:0').length;
    }, { timeout: 5000, intervals: [250] }).toBeGreaterThanOrEqual(2);
    await row.getByRole('button', { name: 'View', exact: true }).click();
    await capture(page, 'operator-console-paused.png');

    await page.setViewportSize({ width: 390, height: 844 });
    await capture(page, 'operator-console-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1000 });

    await page.locator('#operator-confirmation').check();
    await row.getByRole('button', { name: 'Resume' }).click();
    await expect(page.locator('#operator-confirmation')).not.toBeChecked();
    await expect(row).toContainText(/completed/i, { timeout: 120000 });
    await row.getByRole('button', { name: 'View', exact: true }).click();
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
    const canceledBoundsOverride = canceledRow.getByRole('checkbox', { name: /Override .* bounds review/ });
    if (await canceledBoundsOverride.count()) await canceledBoundsOverride.check();
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
    await captureDocumentationNavigation(browser);
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
