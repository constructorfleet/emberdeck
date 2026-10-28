const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

test('API controls stay unavailable and the job count stays unknown before the first load', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#job-count')).toHaveText('—');
  await expect(page.locator('#new-profile')).toBeDisabled();
  await expect(page.locator('#new-project')).toBeDisabled();
  await expect(page.locator('#new-vector-project')).toBeDisabled();
  await expect(page.locator('#refresh')).toBeDisabled();
});

test('rejected key is not stored or polled, then a trimmed correct key connects', async ({ page }) => {
  let apiRequests = 0;
  let driverPolls = 0;
  let releaseValidation;
  let holdValidation = true;
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.pathname.startsWith('/api/v1/')) apiRequests++;
    if (request.method() === 'GET' && url.pathname === '/api/v1/drivers') driverPolls++;
  });
  await page.route('**/api/v1/jobs', async (route) => {
    if (holdValidation) await new Promise((resolve) => { releaseValidation = resolve; });
    await route.continue();
  });
  await page.goto('/');
  await page.locator('#api-key').fill(' wrong-key ');
  await page.locator('#auth-form button[type="submit"]').click();
  const connect = page.locator('#auth-form button[type="submit"]');
  await expect(connect).toContainText('Connecting…');
  await expect(connect).toBeDisabled();
  expect(await page.evaluate(() => sessionStorage.getItem('emberdeck-api-key'))).toBeNull();
  expect(driverPolls).toBe(0);
  await expect.poll(() => Boolean(releaseValidation)).toBe(true);
  holdValidation = false;
  releaseValidation();
  await expect(page.locator('#connection-state')).toHaveText('Not connected', { timeout: 5000 });
  await expect(page.locator('#api-key')).toBeFocused();
  expect(await page.evaluate(() => sessionStorage.getItem('emberdeck-api-key'))).toBeNull();
  const rejectedRequestCount = apiRequests;
  expect(rejectedRequestCount).toBe(1);
  await page.waitForTimeout(2300);
  expect(apiRequests).toBe(rejectedRequestCount);

  await page.locator('#api-key').fill(`  ${apiKey}  `);
  await page.locator('#auth-form button[type="submit"]').click();
  await expect(page.locator('#connection-state')).toContainText('Connected');
  await expect(page.locator('#auth-form')).toBeHidden();
  await expect(page.locator('#connection-controls')).toContainText('Connected');
  await expect(page.locator('#connection-controls')).toContainText('Change key');
  await expect(page.locator('#connection-controls')).toContainText('Disconnect');
  expect(await page.evaluate(() => sessionStorage.getItem('emberdeck-api-key'))).toBe(apiKey);
  await page.locator('#change-key').click();
  await expect(page.locator('#api-key')).toHaveValue(apiKey);
});

test('disconnect forgets the key and stops polling', async ({ page }) => {
  let driverPolls = 0;
  page.on('request', (request) => {
    if (request.method() === 'GET' && new URL(request.url()).pathname === '/api/v1/drivers') driverPolls++;
  });
  await connect(page);
  await expect(page.locator('#new-profile')).toBeEnabled();
  await page.locator('#disconnect').click();
  await expect(page.locator('#auth-form')).toBeVisible();
  await expect(page.locator('#connection-state')).toHaveText('Not connected');
  expect(await page.evaluate(() => sessionStorage.getItem('emberdeck-api-key'))).toBeNull();
  const pollCountAtDisconnect = driverPolls;
  await page.waitForTimeout(2300);
  expect(driverPolls).toBe(pollCountAtDisconnect);
  await expect(page.locator('#new-profile')).toBeDisabled();
  await expect(page.locator('#job-count')).toHaveText('—');
});

test('a 401 during refresh clears a stored key, focuses the key field, and stops polling', async ({ page }) => {
  let rejectedDriverRequests = 0;
  await connect(page);
  expect(await page.evaluate(() => sessionStorage.getItem('emberdeck-api-key'))).toBe(apiKey);
  await page.route('**/api/v1/drivers', async (route) => {
    rejectedDriverRequests++;
    await route.fulfill({ status: 401, contentType: 'application/json', body: '{"error":"authentication required"}' });
  });
  await page.locator('#refresh').click();
  await expect(page.locator('#auth-form')).toBeVisible();
  await expect(page.locator('#connection-state')).toHaveText('Not connected');
  await expect(page.locator('#api-key')).toBeFocused();
  expect(await page.evaluate(() => sessionStorage.getItem('emberdeck-api-key'))).toBeNull();
  expect(rejectedDriverRequests).toBe(1);
  await page.waitForTimeout(2300);
  expect(rejectedDriverRequests).toBe(1);
});

async function connect(page) {
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-controls')).toContainText('Connected');
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
  const boundsOverride = row.getByRole('checkbox', { name: /Override .* bounds review/ });
  if (await boundsOverride.count()) {
    await expect(start).toBeDisabled();
    await boundsOverride.check();
  }
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
  await row.getByRole('button', { name: 'View', exact: true }).click();
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

test('running uploaded G-code shows a fitted camera-free toolpath with estimated controller position', async ({ page }) => {
  await connect(page);
  const name = `operator-toolpath-${test.info().retry}.gcode`;
  const moves = 1000;
  const program = [
    'G21 G90',
    'G0 X0 Y0',
    'M4 S100',
    ...Array.from({ length: moves }, (_, index) => `G1 X${index + 1} Y0 F300`),
    'M5',
  ].join('\n');
  await page.locator('#job-file').setInputFiles({
    name,
    mimeType: 'text/plain',
    buffer: Buffer.from(program),
  });
  await page.locator('#job-name').fill(name);
  await page.getByRole('button', { name: /Add to library/ }).click();
  const row = page.locator('#job-list li').filter({ hasText: name });
  await expect(row).toContainText(/queued/i);
  await expect(row.locator('.toolpath-summary')).toContainText('Static program review · bounds unknown');
  const boundsOverride = row.getByRole('checkbox', { name: /Override unknown bounds review/ });
  await expect(boundsOverride).toBeVisible();
  const start = row.getByRole('button', { name: 'Start' });
  await page.locator('#operator-confirmation').check();
  await expect(start).toBeDisabled();
  await boundsOverride.check();
  await expect(start).toBeEnabled();
  await start.click();
  await expect(row).toContainText(/running/i);
  const progress = row.locator('progress');
  await expect(row.locator('.toolpath-live')).toBeVisible({ timeout: 10000 });
  await expect(row.locator('.toolpath-summary')).toContainText(/estimated/i, { timeout: 10000 });
  await expect(row.locator('.toolpath-svg .path-active')).toHaveCount(1, { timeout: 10000 });
  await expect(row.getByText(/Position match assumes GRBL \$13=0/)).toBeVisible();
  await expect(row.locator('.toolpath-legend')).toContainText('laser-off travel dashed');
  await expect(row.getByRole('button', { name: 'Zoom in toolpath' })).toBeVisible();
  await expect(row.getByRole('button', { name: 'Fit toolpath to view' })).toBeVisible();
  await progress.evaluate((node) => node.value);
  await expect(row).toContainText(/completed/i, { timeout: 30000 });
  await expect(row.locator('.toolpath-summary')).toContainText('controller_idle', { timeout: 10000 });
  await expect(row.locator('.toolpath-svg .path-completed')).toHaveCount(moves, { timeout: 10000 });
});

test('operator pauses on controller acknowledgement, confirms resume, and completes', async ({ page }) => {
  const controllerStates = [];
  const name = `operator-pause-resume-${test.info().repeatEachIndex}-${test.info().retry}.gcode`;
  page.on('response', async (response) => {
    if (response.request().method() !== 'GET' || new URL(response.url()).pathname !== '/api/v1/jobs') return;
    try {
      const jobs = await response.json();
      for (const job of jobs) if (job.name === name && job.controller_state) controllerStates.push(job.controller_state);
    } catch (_) { /* Non-JSON responses are reported by the page assertions. */ }
  });
  await connect(page);
  const lines = 1200;
  const row = await uploadJob(page, name, lines);
  const progress = await startJob(page, row);
  await waitForProgress(progress, lines);
  await row.getByRole('button', { name: 'View', exact: true }).click();

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
  const resumeStateIndex = controllerStates.length;
  await resume.click();
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  await expect.poll(async () => progress.evaluate((node) => node.value), { timeout: 15000 }).toBeGreaterThan(pausedAt);
  await expect.poll(async () => {
    const postResumeStates = controllerStates.slice(resumeStateIndex);
    const text = await row.innerText();
    return postResumeStates.includes('Run') || (postResumeStates.includes('Idle') && /completed/i.test(text));
  }, { timeout: 15000 }).toBe(true);
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
