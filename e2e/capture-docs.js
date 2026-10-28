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
  await page.screenshot({ path: path.join(screenshotDir, name), fullPage: true, scale: 'css' });
}

async function captureElement(page, selector, name, maxHeight = null) {
  await page.evaluate(() => document.activeElement?.blur());
  const output = path.join(screenshotDir, name);
  let bounds;
  let captured = false;
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const element = page.locator(selector);
      await element.waitFor({ state: 'visible' });
      await element.scrollIntoViewIfNeeded();
      bounds = await element.boundingBox();
      if (!bounds) throw new Error(`Cannot capture hidden or empty documentation panel: ${selector}`);
      if (maxHeight && bounds.height > maxHeight) {
        await element.evaluate((node) => window.scrollTo(0, Math.max(0, node.getBoundingClientRect().top)));
        const viewportWidth = await page.evaluate(() => document.documentElement.clientWidth);
        await page.screenshot({ path: output, animations: 'disabled', scale: 'device', clip: { x: 0, y: 0, width: viewportWidth, height: maxHeight } });
      } else {
        await element.screenshot({ path: output, animations: 'disabled', scale: 'device' });
      }
      captured = true;
      break;
    } catch (error) {
      const transient = String(error).includes('not attached to the DOM') || String(error).includes('not stable');
      if (attempt === 2 || !transient) throw error;
      await page.waitForTimeout(50);
    }
  }
  if (!captured) throw new Error(`Could not capture documentation panel: ${selector}`);
  const png = fs.readFileSync(output);
  const width = png.readUInt32BE(16);
  const height = png.readUInt32BE(20);
  if (width < Math.round(bounds.width * 1.9) || height < Math.round(Math.min(bounds.height, maxHeight || bounds.height) * 1.9)) {
    throw new Error(`Documentation panel capture must be 2× (${name}: ${width}×${height}, CSS ${bounds.width}×${bounds.height})`);
  }
}

async function captureDocumentationNavigation(browser) {
  const python = process.env.PYTHON || 'python3';
  const result = spawnSync(python, ['scripts/build-docs.py'], { cwd: root, stdio: 'inherit' });
  if (result.status !== 0) throw new Error('Could not render documentation for navigation screenshots');
  const guide = pathToFileURL(path.join(root, 'dist/docs/operator-guide.html')).href;

  for (const [name, width, height, isMobile] of [
    ['desktop', 1440, 1000, false],
    ['mobile', 375, 812, true],
  ]) {
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: 1, isMobile, hasTouch: isMobile });
    await page.goto(guide);
    await expect(page.getByRole('navigation', { name: 'Documentation' })).toBeVisible();
    await expect(page.locator('nav a[aria-current="page"]')).toHaveAttribute('href', 'operator-guide.html');
    const screenshots = page.locator('img.documentation-screenshot');
    const screenshotCount = await screenshots.count();
    expect(screenshotCount).toBeGreaterThan(10);
    for (const screenshot of await screenshots.all()) {
      await screenshot.scrollIntoViewIfNeeded();
      await expect.poll(() => screenshot.evaluate((image) => image.complete && image.naturalWidth > 0)).toBe(true);
      const details = await screenshot.evaluate((image) => ({
        src: image.getAttribute('src'),
        alt: image.getAttribute('alt'),
        width: image.getAttribute('width'),
        height: image.getAttribute('height'),
        loading: image.getAttribute('loading'),
        parentHref: image.parentElement?.getAttribute('href'),
        imageWidth: image.getBoundingClientRect().width,
        paneWidth: image.parentElement?.getBoundingClientRect().width,
        paneScrollWidth: image.parentElement?.scrollWidth,
        paneLeft: image.parentElement?.getBoundingClientRect().left,
        mainWidth: document.querySelector('main').getBoundingClientRect().width,
        isMobile: image.classList.contains('documentation-screenshot-mobile'),
        imageLeft: image.getBoundingClientRect().left,
        mainLeft: document.querySelector('main').getBoundingClientRect().left,
      }));
      expect(details.alt).toBeTruthy();
      expect(details.width).toMatch(/^\d+$/);
      expect(details.height).toMatch(/^\d+$/);
      expect(details.loading).toBe('lazy');
      expect(details.parentHref).toBe(details.src);
      expect(details.paneWidth).toBeLessThanOrEqual(width - 32 + 1);
      const centerDiff = Math.abs((details.paneLeft + details.paneWidth / 2) - width / 2);
      expect(centerDiff).toBeLessThanOrEqual(2);
      if (isMobile) {
        if (details.imageWidth > details.paneWidth + 1) {
          expect(details.paneScrollWidth).toBeGreaterThan(details.paneWidth);
        }
        const imageHeight = await screenshot.evaluate((image) => image.getBoundingClientRect().height);
        expect(imageHeight).toBeLessThanOrEqual(height * 0.75 + 1);
      }
    }
    const menu = page.locator('nav details').nth(isMobile ? 0 : 3);
    const summary = menu.locator('summary');
    if (isMobile) await summary.tap();
    else await summary.click();
    await expect(menu).toHaveAttribute('open', '');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: path.join(screenshotDir, `docs-navigation-after-${name}.png`) });
    await page.goto(guide);
    const guideImages = await page.locator('img.documentation-screenshot').all();
    for (const image of guideImages) {
      await image.scrollIntoViewIfNeeded();
      await expect.poll(() => image.evaluate((node) => node.complete && node.naturalWidth > 0)).toBe(true);
    }
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: path.join(os.tmpdir(), `emberdeck-operator-guide-${name}.png`), fullPage: true });
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
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 2, reducedMotion: 'reduce' });
    const page = await context.newPage();
    await page.goto(baseURL);
    await page.locator('#api-key').fill(apiKey);
    await page.getByRole('button', { name: /Connect/ }).click();
    await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
    await expect(page.locator('#driver-select')).toHaveValue('simulator');
    await expect(page.locator('#empty-state')).toContainText('Add a G-code file to get started.');
    await page.goto(`${baseURL}/designer`);
    await expect(page.locator('#project-empty')).toContainText('New rectangle project');
    await expect(page.locator('#vector-project-empty')).toContainText('New vector project');

    await page.getByRole('button', { name: 'New profile' }).click();
    await page.getByLabel('Profile name').fill('Documented GRBL');
    await page.getByLabel('Work area width (mm)').fill('300');
    await page.getByLabel('Work area height (mm)').fill('200');
    await page.getByLabel('Maximum spindle value ($30)').fill('1000');
    await page.getByLabel('Minimum feed (mm/min)').fill('1');
    await page.getByLabel('Maximum feed (mm/min)').fill('6000');
    await page.getByLabel('Maximum commanded power (%)').fill('100');
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');
    await captureElement(page, 'section[aria-labelledby="profiles-title"]', 'designer-dedicated-page.png');
    await page.getByRole('button', { name: 'New rectangle project' }).click();
    await expect(page.locator('#project-name')).toBeFocused();
    await captureElement(page, '#project-form', 'designer-keyboard-focus.png');
    await page.keyboard.press('Escape');
    await expect(page.locator('#project-form')).toBeHidden();
    await page.getByRole('button', { name: 'Edit profile' }).click();
    await page.locator('#profile-name').evaluate((input) => input.blur());
    await captureElement(page, '#profile-form', 'designer-profile.png');
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
    await expect(page.locator('#notice .notice-message')).toContainText('Project saved');
    await captureElement(page, '#project-form', 'designer-project.png');
    await page.locator('#rect-x').fill('11');
    await expect(page.locator('#project-save-hint')).toContainText('unsaved edits');
    await captureElement(page, '#project-form', 'designer-unsaved-changes.png');
    page.once('dialog', (dialog) => dialog.accept());
    await page.getByRole('button', { name: 'Close editor' }).click();
    await page.getByRole('button', { name: 'Reopen project Panel outline' }).click();
    await page.locator('#rect-x').fill('290');
    await expect(page.locator('#project-validation')).toContainText(/outside the declared workspace/i);
    await expect(page.locator('#project-preview .engrave-path.out-of-bounds')).toHaveCount(1);
    await expect(page.getByRole('button', { name: 'Save project' })).toBeDisabled();
    await captureElement(page, '#project-form', 'designer-project-out-of-bounds.png');
    await page.locator('#rect-x').fill('10');
    await page.getByRole('button', { name: 'Save project' }).click();
    await expect(page.locator('#notice .notice-message')).toContainText('Project saved');
    await page.getByRole('button', { name: 'Close editor' }).click();
    const reopenProject = page.getByRole('button', { name: 'Reopen project Panel outline' });
    await expect(reopenProject).toBeVisible();
    await reopenProject.focus();
    await page.keyboard.press('Tab');
    await page.keyboard.press('Shift+Tab');
    await page.waitForTimeout(4300);
    await expect(reopenProject).toBeFocused();
    await page.locator('#project-list').screenshot({ path: path.join(screenshotDir, 'designer-project-keyboard-focus.png'), animations: 'disabled' });
    await capture(page, 'designer-console-overview.png');
    await captureElement(page, 'section[aria-labelledby="profiles-title"]', 'designer-console-overview-profile-detail.png');
    await captureElement(page, 'section[aria-labelledby="projects-title"]', 'designer-console-overview-project-detail.png');
    await page.getByRole('button', { name: 'Reopen project Panel outline' }).click();
    await page.getByRole('button', { name: 'Edit profile' }).click();
    await page.getByLabel('Work area width (mm)').fill('290');
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect(page.locator('#stale-profile-warning')).toBeVisible();
    const staleProjectRow = page.locator('#project-list li').filter({ hasText: 'Panel outline' });
    await expect(staleProjectRow).toContainText('Documented GRBL');
    await expect(staleProjectRow).toContainText('r1');
    await expect(staleProjectRow).toContainText('Needs review');
    await captureElement(page, '#project-list li', 'designer-project-needs-review.png');
    await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
    await captureElement(page, '#project-form', 'designer-stale-profile.png');
    await page.getByRole('button', { name: 'Save project' }).click();
    await expect(page.locator('#stale-profile-warning')).toBeHidden();
    await page.getByRole('button', { name: 'Generate queued job' }).click();
    await page.getByRole('link', { name: 'Job library' }).click();
    const designerRow = page.locator('#job-list li').filter({ hasText: 'Panel outline.gcode' });
    await expect(designerRow).toContainText(/queued/i);
    await designerRow.getByRole('button', { name: /^View / }).click();
    await expect(page.locator('#program-provenance')).toContainText('Panel outline');
    await captureElement(page, '#program-provenance', 'designer-generated-review.png');
    const sourceJobID = await designerRow.getByRole('button', { name: /^View / }).getAttribute('data-job-id');
    await page.locator('#close-program').click();
    const runningGenerated = await page.evaluate(async (sourceJobID) => {
      const headers = { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}`, 'Content-Type': 'application/json' };
      const source = await (await fetch(`/api/v1/jobs/${sourceJobID}`, { headers })).json();
      const profile = source.provenance.profile;
      const points = [{ x_mm: 0, y_mm: 0 }, { x_mm: 10, y_mm: 0 }, ...Array.from({ length: 500 }, (_, index) => {
        const row = Math.floor(index / 25);
        const column = index % 25;
        return { x_mm: 10 + (row % 2 === 0 ? column : 24 - column) * 10, y_mm: 10 + row * 9 };
      })];
      const projectResponse = await fetch('/api/v1/projects', {
        method: 'POST', headers,
        body: JSON.stringify({
          schema_version: 2, name: 'Running generated profile view', units: 'mm',
          profile_id: profile.id, profile_revision: profile.revision,
          objects: [{ id: 'running-path', kind: 'path', operation_id: 'running-engrave', subpaths: [{ points, closed: false }] }],
          operations: [{ id: 'running-engrave', type: 'vector-engrave', feed_mm_min: 600, power_percent: 25 }],
        }),
      });
      if (!projectResponse.ok) throw new Error(`Could not create long generated screenshot project: ${projectResponse.status} ${await projectResponse.text()}`);
      const project = await projectResponse.json();
      const jobResponse = await fetch(`/api/v1/projects/${project.id}/generate`, { method: 'POST', headers, body: JSON.stringify({ revision: project.revision }) });
      if (!jobResponse.ok) throw new Error(`Could not generate screenshot job: ${jobResponse.status} ${await jobResponse.text()}`);
      return { project, job: await jobResponse.json() };
    }, sourceJobID);
    await page.locator('#refresh').click();
    const runningGeneratedRow = page.locator('#job-list li').filter({ hasText: runningGenerated.job.name });
    await expect(runningGeneratedRow).toContainText(/queued/i);
    await page.locator('#operator-confirmation').check();
    await runningGeneratedRow.getByRole('button', { name: /^Start / }).click();
    const generatedStart = page.getByRole('dialog', { name: /Start Running generated profile view\.gcode\?/ });
    await generatedStart.getByRole('button', { name: 'Start on simulator' }).click();
    await expect(runningGeneratedRow).toContainText(/running/i);
    await expect(runningGeneratedRow.locator('.toolpath-summary')).toContainText(/Path progress:/, { timeout: 15000 });
    await expect(runningGeneratedRow.locator('.toolpath-segment.path-active')).toHaveCount(1);
    await expect(runningGeneratedRow.locator('.toolpath-profile-bounds')).toHaveCount(1);
    await expect(runningGeneratedRow.locator('.toolpath-legend')).toContainText(/not sensed or verified machine travel/i);
    await captureElement(page, '#job-list li:has-text("Running generated profile view.gcode")', 'operator-console-running-generated-view.png');
    await expect(runningGeneratedRow).toContainText(/completed/i, { timeout: 30000 });
    page.once('dialog', (dialog) => dialog.accept());
    await runningGeneratedRow.getByRole('button', { name: /Delete Running generated profile view\.gcode/ }).click();
    await expect(runningGeneratedRow).toHaveCount(0);
    await page.evaluate(async (projectID) => {
      const response = await fetch(`/api/v1/projects/${projectID}`, { method: 'DELETE', headers: { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ revision: 1 }) });
      if (!response.ok) throw new Error(`Could not remove screenshot project: ${response.status}`);
    }, runningGenerated.project.id);
    await page.getByRole('link', { name: 'Designer' }).click();
    await page.setViewportSize({ width: 375, height: 812 });
    await captureElement(page, 'section[aria-labelledby="projects-title"]', 'designer-mobile.png');
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
    await page.getByRole('button', { name: '1. rectangle' }).click();
    await page.getByLabel('Translate X (mm)').fill('3');
    await page.getByLabel('Translate Y (mm)').fill('2');
    await page.getByLabel('Scale X').fill('1.2');
    await page.getByLabel('Rotation (degrees)').fill('15');
    await expect(page.locator('#vector-validation')).toBeHidden();
    await captureElement(page, '#vector-transform', 'designer-vector-transform.png');
    const alignmentChecks = page.getByRole('checkbox', { name: /Select shape/ });
    await alignmentChecks.nth(0).check();
    await alignmentChecks.nth(1).check();
    await expect(page.getByRole('button', { name: 'Align left' })).toBeEnabled();
    await page.getByRole('button', { name: 'Align left' }).click();
    await expect(page.locator('#vector-align-status')).toContainText('2 shapes aligned left');
    await captureElement(page, '#vector-align-controls', 'designer-vector-alignment.png');
    await page.getByRole('button', { name: 'Save vector project' }).click();
    await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');
    await captureElement(page, '#vector-object-list', 'designer-vector-project.png');
    await page.locator('#generate-vector-project').click();
    await page.getByRole('link', { name: 'Job library' }).click();
    const vectorRow = page.locator('#job-list li').filter({ hasText: 'Three shapes, two operations.gcode' });
    await expect(vectorRow).toContainText(/queued/i);
    await vectorRow.getByRole('button', { name: /^View / }).click();
    await expect(page.locator('#program-provenance')).toContainText('2. Cut');
    await captureElement(page, '#program-provenance', 'designer-vector-review.png');
    await page.getByRole('link', { name: 'Designer' }).click();
    await page.getByRole('button', { name: 'Reopen vector project Three shapes, two operations' }).click();
    await page.setViewportSize({ width: 375, height: 812 });
    await captureElement(page, '#vector-object-list', 'designer-vector-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.getByRole('button', { name: 'New vector project' }).click();
    await page.locator('#vector-name').fill('Monoline text engraving');
    await page.getByRole('button', { name: 'Add text' }).click();
    await page.locator('#vector-text-content').fill('EMBER 42');
    await page.locator('#vector-text-size').fill('18');
    await expect(page.locator('#vector-validation')).toBeHidden();
    await captureElement(page, '#vector-text-fields', 'designer-monoline-text.png');
    await page.setViewportSize({ width: 375, height: 812 });
    await captureElement(page, '#vector-text-fields', 'designer-monoline-text-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1000 });
    page.once('dialog', (dialog) => dialog.accept());
    await page.locator('#close-vector-project').click();
    await page.getByRole('button', { name: 'New vector project' }).click();
    await page.locator('#vector-name').fill('Three shapes, two operations');
    await page.getByRole('button', { name: 'Add rectangle' }).click();
    await page.getByRole('button', { name: 'Add ellipse' }).click();
    await page.getByRole('button', { name: 'Add line' }).click();
    await page.getByRole('button', { name: 'Add layer' }).click();
    await page.getByRole('combobox', { name: 'Operation 2 type' }).selectOption('vector-cut');
    await page.getByRole('button', { name: '2. ellipse' }).click();
    await page.locator('#vector-object-operation').selectOption({ label: '2. cut' });
    await page.getByRole('button', { name: '1. rectangle' }).click();
    await page.getByRole('checkbox', { name: /Select shape 1 rectangle/ }).check();
    await page.getByRole('checkbox', { name: /Select shape 2 ellipse/ }).check();
    await page.setViewportSize({ width: 375, height: 812 });
    await captureElement(page, '#vector-align-controls', 'designer-vector-alignment-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1000 });
    page.once('dialog', (dialog) => dialog.accept());
    await page.locator('#close-vector-project').click();
    await page.getByRole('button', { name: 'New vector project' }).click();
    await page.locator('#vector-name').fill('Imported SVG paths');
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="90mm" height="55mm" viewBox="0 0 90 55"><g id="outer" transform="translate(5 2)"><g id="inner" transform="translate(5 0)"><path d="M10 45 L35 45 L35 25 L10 25 Z" transform="translate(2 0)"/><path d="M50 45 C58 25 72 25 80 45"/></g></g><text x="8" y="18" font-family="emberdeck-stick-v1" font-size="12">DECK 42</text></svg>`;
    await page.locator('#vector-svg-file').setInputFiles({ name: 'imported-shapes.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(svg) });
    await expect(page.locator('#notice .notice-message')).toContainText('Imported 3 editable SVG paths');
    await expect(page.locator('#vector-object-list li')).toHaveCount(3);
    await captureElement(page, '#vector-object-list', 'designer-svg-import.png');
    await captureElement(page, '#vector-preview', 'designer-svg-import-preview.png');
    await page.setViewportSize({ width: 375, height: 812 });
    await captureElement(page, '#vector-object-list', 'designer-svg-import-mobile.png');
    await captureElement(page, '#vector-preview', 'designer-svg-import-preview-mobile.png');
    await page.setViewportSize({ width: 1440, height: 1000 });
    page.once('dialog', (dialog) => dialog.accept());
    await page.locator('#close-vector-project').click();
    const vectorProject = page.locator('#vector-project-list li').filter({ hasText: 'Three shapes, two operations' });
    page.once('dialog', (dialog) => dialog.accept());
    await vectorProject.getByRole('button', { name: 'Delete' }).click();
    await expect(vectorProject).toHaveCount(0);

    const projectRow = page.locator('#project-list li').filter({ hasText: 'Panel outline' });
    page.once('dialog', (dialog) => dialog.accept());
    await projectRow.getByRole('button', { name: 'Delete project Panel outline' }).click();
    await expect(projectRow).toHaveCount(0);
    await page.getByRole('link', { name: 'Job library' }).click();
    page.once('dialog', (dialog) => dialog.accept());
    await vectorRow.getByRole('button', { name: /Delete Three shapes, two operations\.gcode/ }).click();
    await expect(vectorRow).toHaveCount(0);
    page.once('dialog', (dialog) => dialog.accept());
    await designerRow.getByRole('button', { name: /Delete Panel outline\.gcode/ }).click();
    await expect(designerRow).toHaveCount(0);

    // Controller settings read with $$, a refused profile mismatch, and an M3 warning.
    const docsApi = (method, path, body) => page.evaluate(async ({ method, path, body }) => {
      const response = await fetch(path, { method, headers: { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
      if (!response.ok) throw new Error(`${method} ${path}: ${response.status} ${await response.text()}`);
      return response.status === 204 ? null : response.json();
    }, { method, path, body });
    const mismatchProfile = await docsApi('POST', '/api/v1/profiles', { schema_version: 1, name: 'Mismatched 8-bit PWM', controller: 'grbl-1.1', units: 'mm', origin: 'lower-left', width_mm: 300, height_mm: 200, min_s: 0, max_s: 255, min_feed_mm_min: 1, max_feed_mm_min: 6000, max_power_percent: 100, laser_mode_enabled: true });
    const mismatchProject = await docsApi('POST', '/api/v1/projects', { schema_version: 1, name: 'Settings mismatch', profile_id: mismatchProfile.id, profile_revision: 1, rectangle: { x_mm: 10, y_mm: 10, width_mm: 20, height_mm: 20 }, operation: { type: 'vector-engrave', feed_mm_min: 600, power_percent: 10 } });
    const mismatchJob = await docsApi('POST', `/api/v1/projects/${mismatchProject.id}/generate`, { revision: 1 });
    const constantJob = await docsApi('POST', '/api/v1/jobs', { name: 'constant-power-m3.gcode', gcode: 'G21\nG90\nG0 X10 Y10\nM3 S300\nG1 X40 Y10 F600\nM5\n' });
    await page.locator('#refresh').click();
    await expect(page.locator('#controller-settings')).toContainText('$32=1 (laser mode)');
    await expect(page.locator('#job-list li').filter({ hasText: 'Settings mismatch.gcode' })).toContainText('Start will be refused');
    await expect(page.locator('#job-list li').filter({ hasText: 'constant-power-m3.gcode' })).toContainText('Program uses M3 constant power');
    await captureElement(page, '.driver-controls', 'operator-console-controller-settings.png');
    await page.route('**/api/v1/drivers', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([{ id: 'simulator', busy: false, last_seen: new Date().toISOString(), capabilities: ['manual_home', 'manual_unlock', 'manual_jog', 'manual_set_zero', 'manual_frame'], controller_state: 'Idle', camera_available: false }]),
    }));
    await page.route('**/api/v1/jobs?*', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ jobs: [{ id: 'manual-frame-guide', name: 'Reviewed frame sample.gcode', state: 'queued', created: new Date().toISOString(), acked: 0, total: 0, program_review: { bounds_status: 'in_bounds', bounds: { min: { x: 10, y: 10 }, max: { x: 30, y: 25 } } } }], total: 1 }),
    }));
    await page.locator('#refresh').click();
    await page.locator('#manual-confirmation').check();
    await expect(page.locator('#manual-frame-job option').filter({ hasText: 'Reviewed frame sample.gcode' })).toHaveCount(1);
    await page.locator('#manual-frame-job').selectOption({ label: 'Reviewed frame sample.gcode' });
    await expect(page.locator('#manual-home')).toBeEnabled();
    await expect(page.locator('#manual-frame')).toBeEnabled();
    await captureElement(page, '.manual-controls', 'operator-console-manual-controls.png');
    await page.unroute('**/api/v1/drivers');
    await page.unroute('**/api/v1/jobs?*');
    await docsApi('DELETE', `/api/v1/jobs/${mismatchJob.id}`);
    await docsApi('DELETE', `/api/v1/jobs/${constantJob.id}`);
    await docsApi('DELETE', `/api/v1/projects/${mismatchProject.id}`, { revision: 1 });
    await docsApi('DELETE', `/api/v1/profiles/${mismatchProfile.id}`, { revision: 1 });
    await page.locator('#refresh').click();
    await expect(page.locator('#job-list li').filter({ hasText: 'constant-power-m3.gcode' })).toHaveCount(0);

    const lines = 1000;
    const name = 'documented-controller-demo.gcode';
    const program = `G21\nG90\nG0 X0 Y0\nM4 S100\n${Array.from({ length: lines }, (_, i) => `G1 X${i + 1} Y${Math.floor(i / 50)} F300`).join('\n')}\nM5\n`;
    await page.locator('#job-file').setInputFiles({ name, mimeType: 'text/plain', buffer: Buffer.from(program) });
    await page.locator('#job-name').fill(name);
    await page.getByRole('button', { name: /Add to library/ }).click();
    const row = page.locator('#job-list li').filter({ hasText: name });
    await expect(row).toContainText(/queued/i);
    await row.getByRole('button', { name: /^View / }).click();
    await page.locator('#close-program').click();
    await expect(row.locator('.toolpath-summary')).toContainText(/bounds out_of_bounds/i);
    await expect(row.locator('.toolpath-profile-bounds')).toHaveCount(1);
    await expect(row.getByRole('checkbox', { name: /Override out_of_bounds bounds review/ })).toBeVisible();
    await captureElement(page, '#job-list .job-item', 'operator-console-uploaded-bounds-review.png');
    await expect(row).toContainText(/created/i);
    await expect(row).toContainText(/bytes/i);
    await expect(row).toContainText('Uploaded');
    await captureElement(page, '#job-list .job-item', 'operator-console-library.png');

    await page.locator('#operator-confirmation').check();
    const boundsOverride = row.getByRole('checkbox', { name: /Override .* bounds review/ });
    if (await boundsOverride.count()) await boundsOverride.check();
    await row.getByRole('button', { name: /^Start / }).click();
    const startDialog = page.getByRole('dialog', { name: `Start ${name}?` });
    await expect(startDialog).toBeVisible();
    await captureElement(page, '#action-dialog', 'operator-console-start-confirm.png');
    await startDialog.getByRole('button', { name: 'Start on simulator' }).click();
    const progress = row.locator('progress');
    await expect.poll(() => progress.evaluate((node) => node.value), { timeout: 15000 }).toBeGreaterThan(0);
    await row.getByRole('button', { name: /^View / }).click();
    await expect(page.locator('#program-panel')).toBeVisible();
    await expect(row.locator('.toolpath-live')).toBeVisible();
    await expect(row.locator('.toolpath-summary')).toContainText(/estimated/i);
    await captureElement(page, '#job-list .job-item', 'operator-console-running-view.png');

    await row.getByRole('button', { name: /^Pause / }).click();
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
    await row.getByRole('button', { name: /^View / }).click();
    await captureElement(page, '#job-list .job-item', 'operator-console-paused.png');

    await page.setViewportSize({ width: 375, height: 812 });
    await captureElement(page, '#job-list .job-item', 'operator-console-mobile.png', 280);
    await page.setViewportSize({ width: 1440, height: 1000 });

    await page.locator('#operator-confirmation').check();
    await row.getByRole('button', { name: /^Resume / }).click();
    await expect(page.locator('#operator-confirmation')).not.toBeChecked();
    await expect(row.locator('.job-info > span').first()).toContainText(/^completed\b/i, { timeout: 120000 });
    await row.getByRole('button', { name: /^View / }).click();
    // Job polling rebuilds each row every two seconds. Capture the persistent
    // list container so a refresh cannot detach the screenshot target.
    await captureElement(page, '#job-list', 'operator-console-completed.png');
    const history = page.getByRole('list', { name: 'Job event history' });
    // A short run can finish before a Run report, recording resuming → completed.
    await expect(history.getByRole('listitem').last().locator('.job-history-states')).toHaveText(/^(running|resuming) → completed$/, { timeout: 10000 });
    await expect(row.locator('.job-times')).toContainText('Duration');
    await captureElement(page, '#program-history', 'operator-console-job-history.png');

    page.once('dialog', (dialog) => dialog.accept());
    await row.getByRole('button', { name: /^Delete / }).click();
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
    await canceledRow.getByRole('button', { name: /^Start / }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Start on simulator' }).click();
    const canceledProgress = canceledRow.locator('progress');
    await expect.poll(() => canceledProgress.evaluate((node) => node.value), { timeout: 15000 }).toBeGreaterThan(0);
    await canceledRow.getByRole('button', { name: /^Pause / }).click();
    await expect(canceledRow).toContainText(/paused/i, { timeout: 15000 });
    await expect(canceledRow).toContainText('Last controller report: Hold:0');
    await canceledRow.getByRole('button', { name: /^Cancel / }).click();
    const cancelDialog = page.getByRole('dialog', { name: `Cancel ${canceledName}?` });
    await expect(cancelDialog).toBeVisible();
    await captureElement(page, '#action-dialog', 'operator-console-cancel-confirm.png');
    await cancelDialog.getByRole('button', { name: 'Cancel job' }).click();
    await expect(canceledRow).toContainText(/canceled/i, { timeout: 10000 });
    await expect(canceledRow.getByRole('button', { name: /^Delete / })).toBeVisible();
    await canceledRow.getByRole('button', { name: /^View / }).click();
    await captureElement(page, '#job-list .job-item', 'operator-console-canceled.png');
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
