const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

async function connect(page) {
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await expect(page.locator('#driver-select')).toHaveValue('simulator');
}

async function uploadJob(page, name, lineCount) {
  const program = `${Array.from({ length: lineCount }, () => 'G1 X1 F100').join('\n')}\n`;
  await page.locator('#job-file').setInputFiles({
    name: `${name}.gcode`,
    mimeType: 'text/plain',
    buffer: Buffer.from(program),
  });
  await page.locator('#job-name').fill(name);
  await page.getByRole('button', { name: /Add to library/ }).click();
  const row = page.locator('#job-list li').filter({ hasText: name });
  await expect(row).toContainText(/queued/i);
  await expect(page.locator('#empty-state')).toBeHidden();
  return row;
}

async function startJob(page, row) {
  const start = row.getByRole('button', { name: 'Start' });
  await expect(start).toBeDisabled();
  await page.locator('#operator-confirmation').check();
  await expect(start).toBeEnabled();
  await start.click();
  await expect(row).toContainText(/running/i);
  return row.locator('progress');
}

async function waitForProgress(progress, total) {
  await expect.poll(async () => progress.evaluate((node) => node.value), { timeout: 15000 }).toBeGreaterThan(0);
  const acked = await progress.evaluate((node) => node.value);
  expect(acked).toBeLessThan(total);
}

test('operator uploads, confirms, monitors, views, completes, and deletes a job', async ({ page }) => {
  let driverPolls = 0;
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (request.method() === 'GET' && url.pathname === '/api/v1/drivers') driverPolls++;
  });
  await connect(page);

  const lines = 1200;
  const name = `operator-completion-${test.info().retry}.gcode`;
  const row = await uploadJob(page, name, lines);
  const progress = await startJob(page, row);
  await waitForProgress(progress, lines);

  const selectedDriver = await page.locator('#driver-select').inputValue();
  const pollCountBeforeView = driverPolls;
  await row.getByRole('button', { name: 'View' }).click();
  await expect(page.locator('#program-panel')).toBeVisible();
  await expect(page.locator('#program-code')).toContainText('G1 X1 F100');
  await expect.poll(() => driverPolls, { timeout: 5000 }).toBeGreaterThan(pollCountBeforeView);
  await expect(page.locator('#program-panel')).toBeVisible();
  await expect(page.locator('#driver-select')).toHaveValue(selectedDriver);

  await expect(row).toContainText(/completed/i, { timeout: 30000 });
  await expect(progress).toHaveJSProperty('value', lines);
  page.once('dialog', (dialog) => dialog.accept());
  await row.getByRole('button', { name: /Delete/ }).click();
  await expect(page.locator('#notice')).toHaveText('Program deleted.');
  await expect(page.locator('#job-list li')).toHaveCount(0);
  await expect(page.locator('#empty-state')).toBeVisible();
});

test('operator can cancel a running job', async ({ page }) => {
  await connect(page);
  const lines = 1000;
  const row = await uploadJob(page, `operator-cancel-${test.info().retry}.gcode`, lines);
  const progress = await startJob(page, row);
  await waitForProgress(progress, lines);
  await row.getByRole('button', { name: 'Cancel' }).click();
  await expect(row).toContainText(/canceled/i, { timeout: 10000 });
  await expect(row.getByRole('button', { name: /Delete/ })).toBeVisible();
});

test('operator pauses on controller acknowledgement, confirms resume, and completes', async ({ page }) => {
  const controllerStates = [];
  page.on('response', async (response) => {
    if (response.request().method() !== 'GET' || new URL(response.url()).pathname !== '/api/v1/jobs') return;
    try {
      const jobs = await response.json();
      for (const job of jobs) if (job.controller_state) controllerStates.push(job.controller_state);
    } catch (_) { /* Non-JSON responses are reported by the page assertions. */ }
  });
  await connect(page);
  const lines = 1200;
  const row = await uploadJob(page, `operator-pause-resume-${test.info().retry}.gcode`, lines);
  const progress = await startJob(page, row);
  await waitForProgress(progress, lines);
  await row.getByRole('button', { name: 'View' }).click();

  await row.getByRole('button', { name: 'Pause' }).click();
  await expect(row).toContainText(/pausing/i);
  await expect(row.getByRole('button', { name: 'Pausing…' })).toBeDisabled();
  await expect(row).toContainText(/paused/i, { timeout: 15000 });
  await expect(row).toContainText('Last controller report: Hold:0');
  await expect(row.getByRole('button', { name: 'Resume' })).toBeVisible();
  await expect.poll(() => controllerStates.filter((state) => state === 'Hold:0').length, { timeout: 5000 }).toBeGreaterThanOrEqual(2);
  const pausedAt = await progress.evaluate((node) => node.value);
  await page.waitForTimeout(2500);
  await expect(progress).toHaveJSProperty('value', pausedAt);
  await expect(page.locator('#program-panel')).toBeVisible();
  await expect(page.locator('#driver-select')).toHaveValue('simulator');

  const resume = row.getByRole('button', { name: 'Resume' });
  await expect(resume).toBeDisabled();
  await page.locator('#operator-confirmation').check();
  await expect(resume).toBeEnabled();
  await resume.click();
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  await expect(row).toContainText(/resuming/i);
  await expect(row).toContainText(/running/i, { timeout: 15000 });
  await expect(row).toContainText(/Run/);
  await expect(row).toContainText(/completed/i, { timeout: 30000 });
  await expect(progress).toHaveJSProperty('value', lines);
});

test('operator can cancel a paused job and delete the interrupted job', async ({ page }) => {
  await connect(page);
  const row = await uploadJob(page, `operator-cancel-paused-${test.info().retry}.gcode`, 1000);
  const progress = await startJob(page, row);
  await waitForProgress(progress, 1000);
  await row.getByRole('button', { name: 'Pause' }).click();
  await expect(row).toContainText(/paused/i, { timeout: 15000 });
  await expect(row).toContainText('Last controller report: Hold:0');
  await expect(row.getByRole('button', { name: 'Delete' })).toHaveCount(0);

  await row.getByRole('button', { name: 'Cancel' }).click();
  await expect(row).toContainText(/canceled/i, { timeout: 10000 });
  await expect(row.getByRole('button', { name: 'Delete' })).toBeVisible();
  page.once('dialog', (dialog) => dialog.accept());
  await row.getByRole('button', { name: 'Delete' }).click();
  await expect(row).toHaveCount(0);
});
