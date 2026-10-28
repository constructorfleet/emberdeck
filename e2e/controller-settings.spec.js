const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

async function connect(page) {
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-controls')).toContainText('Connected');
  await expect(page.locator('#driver-select')).toHaveValue('simulator');
}

// api performs setup through the authenticated HTTP API from the page.
async function api(page, path, body) {
  return page.evaluate(async ({ path, body, key }) => {
    const response = await fetch(path, {
      method: 'POST',
      headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    const data = await response.json();
    if (!response.ok) throw new Error(`${path}: ${response.status} ${JSON.stringify(data)}`);
    return data;
  }, { path, body, key: apiKey });
}

async function generateJob(page, name, maxS) {
  const profile = await api(page, '/api/v1/profiles', {
    schema_version: 1, name: `${name} profile`, controller: 'grbl-1.1', units: 'mm', origin: 'lower-left',
    width_mm: 300, height_mm: 200, min_s: 0, max_s: maxS, min_feed_mm_min: 1, max_feed_mm_min: 6000,
    max_power_percent: 100, laser_mode_enabled: true,
  });
  const project = await api(page, '/api/v1/projects', {
    schema_version: 1, name, profile_id: profile.id, profile_revision: profile.revision,
    rectangle: { x_mm: 10, y_mm: 10, width_mm: 20, height_mm: 20 },
    operation: { type: 'vector-engrave', feed_mm_min: 600, power_percent: 10 },
  });
  await api(page, `/api/v1/projects/${project.id}/generate`, { revision: project.revision });
  return `${name}.gcode`;
}

function jobRow(page, name) {
  return page.locator('#job-list li').filter({ hasText: name });
}

test('console shows the simulator controller settings read with $$', async ({ page }) => {
  await connect(page);
  const settings = page.locator('#controller-settings');
  await expect(settings).toBeVisible();
  await expect(settings).toContainText('$30=1000 (max spindle)', { timeout: 15000 });
  await expect(settings).toContainText('$31=0 (min spindle)');
  await expect(settings).toContainText('$32=1 (laser mode)');
  await expect(settings).toContainText('$20=0 (soft limits)');
  await expect(settings).toContainText('$130=300 (X max travel mm)');
});

test('Start refuses a generated job whose profile differs from the controller settings', async ({ page }) => {
  const startResponses = [];
  page.on('response', (response) => {
    if (response.request().method() === 'POST' && /\/api\/v1\/jobs\/[^/]+\/start$/.test(new URL(response.url()).pathname)) startResponses.push(response.status());
  });
  await connect(page);
  await expect(page.locator('#controller-settings')).toContainText('$30=1000', { timeout: 15000 });
  const name = `Settings mismatch ${test.info().retry}-${Date.now()}`;
  const jobName = await generateJob(page, name, 255);
  await page.getByLabel('Name', { exact: true }).fill(name);
  const row = jobRow(page, jobName);
  await expect(row).toContainText(/queued/i);
  await expect(row.locator('.laser-mode-warning')).toHaveText(/differ from this job's profile snapshot: \$30 \(maximum spindle\): controller 1000, profile 255\. Start will be refused\./);
  // Record every notice text to ensure refreshes do not overwrite action feedback.
  await page.evaluate(() => {
    window.noticeTexts = [];
    const notice = document.querySelector('#notice');
    new MutationObserver(() => window.noticeTexts.push(notice.textContent)).observe(notice, { childList: true, characterData: true, subtree: true });
  });
  await page.locator('#operator-confirmation').check();
  await row.getByRole('button', { name: /^Start / }).click();
  const dialog = page.getByRole('dialog', { name: /^Start .*\?$/ });
  await expect(dialog).toContainText('$30=1000');
  await expect(dialog).toContainText('$30 (maximum spindle): controller 1000, profile 255. Start will be refused.');
  await dialog.getByRole('button', { name: 'Start on simulator' }).click();
  await expect(dialog).toBeHidden();
  await expect.poll(() => startResponses).toEqual([409]);
  await expect.poll(() => page.evaluate(() => window.noticeTexts.find((text) => text.includes('profile snapshot')) || '')).toContain('$30 (maximum spindle): controller 1000, profile 255');
  const refusal = await page.evaluate(() => window.noticeTexts.find((text) => text.includes('profile snapshot')));
  expect(refusal).toContain("controller settings on driver simulator differ from the job's profile snapshot");
  expect(refusal).not.toContain('$32');
  await expect(row).toContainText(/queued/i);
});

test('uploaded M3 program warns about constant laser power before Start', async ({ page }) => {
  await connect(page);
  const name = `constant-power-${test.info().retry}-${Date.now()}.gcode`;
  await api(page, '/api/v1/jobs', { name, gcode: 'G21\nG90\nM3 S100\nG1 X1 F100\nM5\n' });
  await page.getByLabel('Name', { exact: true }).fill(name);
  const row = jobRow(page, name);
  await expect(row).toContainText(/queued/i);
  await expect(row.locator('.laser-mode-warning')).toHaveText(/Program uses M3 constant power/);
  await expect(row.locator('.laser-mode-warning')).toHaveCount(1);
  await page.locator('#operator-confirmation').check();
  await row.getByRole('checkbox', { name: /Override .* bounds review/ }).check();
  await row.getByRole('button', { name: /^Start / }).click();
  const dialog = page.getByRole('dialog', { name: /^Start .*\?$/ });
  await expect(dialog).toContainText('Program uses M3 constant power');
  await dialog.getByRole('button', { name: 'Go back' }).click();
  await expect(dialog).toBeHidden();
  await expect(row).toContainText(/queued/i);
});

test('uploaded job warns when the selected driver reports $32=0', async ({ page }) => {
  // The simulator reports $32=1; substitute a laser-mode-off report.
  await page.route('**/api/v1/drivers', async (route) => {
    const response = await route.fetch();
    const drivers = await response.json();
    for (const driver of drivers) {
      if (driver.controller_settings) driver.controller_settings['$32'] = 0;
    }
    await route.fulfill({ response, json: drivers });
  });
  await connect(page);
  await expect(page.locator('#controller-settings')).toContainText('$32=0 (laser mode)', { timeout: 15000 });
  const name = `laser-mode-off-${test.info().retry}-${Date.now()}.gcode`;
  await api(page, '/api/v1/jobs', { name, gcode: 'G21\nG90\nM4 S100\nG1 X1 F100\nM5\n' });
  await page.getByLabel('Name', { exact: true }).fill(name);
  const row = jobRow(page, name);
  await expect(row).toContainText(/queued/i);
  await expect(row.locator('.laser-mode-warning')).toHaveText(/reports \$32=0 \(laser mode off\)/);
  await expect(row.locator('.laser-mode-warning')).toHaveCount(1);
});
