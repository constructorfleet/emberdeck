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
  const start = row.getByRole('button', { name: /^Start / });
  await expect(start).toBeDisabled();
  await page.locator('#operator-confirmation').check();
  const boundsOverride = row.getByRole('checkbox', { name: /Override .* bounds review/ });
  if (await boundsOverride.count()) {
    await expect(start).toBeDisabled();
    await boundsOverride.check();
  }
  await expect(start).toBeEnabled();
  await confirmStart(page, start);
  await expect(row).toContainText(/running/i);
  return row.locator('progress');
}

async function confirmStart(page, start, driver = 'simulator') {
  await start.click();
  const dialog = page.getByRole('dialog', { name: /^Start .*\?$/ });
  await expect(dialog).toContainText(driver);
  await dialog.getByRole('button', { name: `Start on ${driver}` }).click();
  await expect(dialog).toBeHidden();
}

async function confirmCancel(page, cancel) {
  await cancel.click();
  const dialog = page.getByRole('dialog', { name: /^Cancel .*\?$/ });
  await dialog.getByRole('button', { name: 'Cancel job' }).click();
  await expect(dialog).toBeHidden();
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
  await row.getByRole('button', { name: /^View / }).click();
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
  await confirmCancel(page, row.getByRole('button', { name: /^Cancel / }));
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
  const start = row.getByRole('button', { name: /^Start / });
  await page.locator('#operator-confirmation').check();
  await expect(start).toBeDisabled();
  await boundsOverride.check();
  await expect(start).toBeEnabled();
  await confirmStart(page, start);
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
      const { jobs } = await response.json();
      for (const job of jobs) if (job.name === name && job.controller_state) controllerStates.push(job.controller_state);
    } catch (_) { /* Non-JSON responses are reported by the page assertions. */ }
  });
  await connect(page);
  const lines = 1200;
  const row = await uploadJob(page, name, lines);
  const progress = await startJob(page, row);
  await waitForProgress(progress, lines);
  await row.getByRole('button', { name: /^View / }).click();

  const pause = row.getByRole('button', { name: `Pause ${name}` });
  await expect(pause).toHaveAccessibleDescription(/without canceling it; Resume continues it/);
  await pause.click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(row).toContainText(/pausing/i);
  await expect(row.getByRole('button', { name: /^Pausing… / })).toBeDisabled();
  await expect(row).toContainText(/paused/i, { timeout: 15000 });
  await expect(row).toContainText('Last controller report: Hold:0');
  await expect(row.getByRole('button', { name: /^Resume / })).toBeVisible();
  await expect.poll(() => controllerStates.filter((state) => state === 'Hold:0').length, { timeout: 5000 }).toBeGreaterThanOrEqual(2);
  const pausedAt = await progress.evaluate((node) => node.value);
  await page.waitForTimeout(2500);
  await expect(progress).toHaveJSProperty('value', pausedAt);
  await expect(page.locator('#program-panel')).toBeVisible();
  await expect(page.locator('#driver-select')).toHaveValue('simulator');

  const resume = row.getByRole('button', { name: `Resume ${name}` });
  await expect(resume).toBeDisabled();
  await expect(resume).toHaveAccessibleDescription(/Needs the safety check/);
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
  await row.getByRole('button', { name: /^Pause / }).click();
  await expect(row).toContainText(/paused/i, { timeout: 15000 });
  await expect(row).toContainText('Last controller report: Hold:0');
  await expect(row.getByRole('button', { name: /^Delete / })).toHaveCount(0);

  await confirmCancel(page, row.getByRole('button', { name: /^Cancel / }));
  await expect(row).toContainText(/canceled/i, { timeout: 10000 });
  await expect(row.getByRole('button', { name: /^Delete / })).toBeVisible();
  page.once('dialog', (dialog) => dialog.accept());
  await row.getByRole('button', { name: /^Delete / }).click();
  await expect(row).toHaveCount(0);
});

test('operator pages and filters the library while programs load only on view', async ({ page }) => {
  const listBodies = [];
  page.on('response', async (response) => {
    if (response.request().method() !== 'GET' || new URL(response.url()).pathname !== '/api/v1/jobs') return;
    try { listBodies.push(await response.text()); } catch (_) { /* Navigation can discard bodies. */ }
  });
  await connect(page);
  const prefix = `paged-${test.info().repeatEachIndex}-${test.info().retry}-${Date.now()}`;
  await page.evaluate(async ({ prefix, key }) => {
    for (let i = 0; i < 21; i += 1) {
      const response = await fetch('/api/v1/jobs', {
        method: 'POST',
        headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: `${prefix}-${String(i).padStart(2, '0')}.gcode`, gcode: 'G21\nG90\nM5\n' }),
      });
      if (response.status !== 201) throw new Error(`upload ${i}: ${response.status}`);
    }
  }, { prefix, key: apiKey });

  await page.getByLabel('Name', { exact: true }).fill(prefix);
  const rows = page.locator('#job-list li');
  await expect(page.locator('#jobs-range')).toHaveText('1–20 of 21');
  await expect(rows).toHaveCount(20);
  await expect(rows.first()).toContainText(`${prefix}-20.gcode`);
  await expect(page.getByRole('button', { name: '← Newer' })).toBeDisabled();

  await page.getByRole('button', { name: 'Older →' }).click();
  await expect(page.locator('#jobs-range')).toHaveText('21–21 of 21');
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText(`${prefix}-00.gcode`);
  await expect(page.getByRole('button', { name: 'Older →' })).toBeDisabled();
  await rows.first().getByRole('button', { name: /^View / }).click();
  await expect(page.locator('#program-code')).toHaveText('G21\nG90\nM5\n');

  await page.getByLabel('State', { exact: true }).selectOption({ label: 'Completed' });
  await expect(page.locator('#empty-state')).toContainText('No matching programs');
  await expect(page.locator('#job-count')).toHaveText('00');
  await page.getByLabel('State', { exact: true }).selectOption({ label: 'All states' });
  await page.getByLabel('Name', { exact: true }).fill('');
  await expect(page.locator('#job-pager')).toBeVisible();

  expect(listBodies.length).toBeGreaterThan(0);
  for (const body of listBodies) {
    expect(body).not.toContain('"gcode"');
    expect(body).not.toContain('"lease"');
  }
});

test('Start explains why it is unavailable, then confirms the job and driver before dispatch', async ({ page }) => {
  const startRequests = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && /\/api\/v1\/jobs\/[^/]+\/start$/.test(new URL(request.url()).pathname)) startRequests.push(request);
  });
  // No driver worker is connected until the route is removed.
  await page.route('**/api/v1/drivers', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }));
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#driver-select')).toContainText('No drivers connected');
  const name = `operator-dispatch-confirm-${test.info().retry}.gcode`;
  const row = await uploadJob(page, name, 20);

  const view = row.getByRole('button', { name: `View ${name}` });
  const start = row.getByRole('button', { name: `Start ${name}` });
  await expect(view).toBeVisible();
  await expect(row.getByRole('button', { name: `Delete ${name}` })).toBeVisible();
  await page.locator('#operator-confirmation').check();
  const boundsOverride = row.getByRole('checkbox', { name: /Override .* bounds review/ });
  if (await boundsOverride.count()) await boundsOverride.check();

  await expect(start).toHaveAttribute('aria-disabled', 'true');
  await expect(start).not.toHaveAttribute('disabled', /.*/);
  await expect(start).toHaveAccessibleDescription(/Needs a connected driver/);
  await expect(row.getByText('Needs a connected driver')).toBeVisible();
  await view.focus();
  await page.keyboard.press('Tab');
  await expect(start).toBeFocused();
  await page.keyboard.press('Enter');
  await start.click({ force: true });
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(startRequests).toHaveLength(0);

  await page.unroute('**/api/v1/drivers');
  await expect(page.locator('#driver-select')).toHaveValue('simulator', { timeout: 5000 });
  await expect(start).not.toHaveAttribute('aria-disabled', 'true');
  await start.click();
  const dialog = page.getByRole('dialog', { name: `Start ${name}?` });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText(name);
  await expect(dialog).toContainText('simulator');
  await expect(dialog.getByRole('button', { name: 'Go back' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(start).toBeFocused();
  await expect(row).toContainText(/queued/i);
  expect(startRequests).toHaveLength(0);

  await start.click();
  await dialog.getByRole('button', { name: 'Start on simulator' }).click();
  await expect(row).toContainText(/running|completed/i);
  expect(startRequests).toHaveLength(1);
  expect(startRequests[0].postDataJSON()).toMatchObject({ driver_id: 'simulator', confirmed: true });
});

test('Cancel asks for confirmation naming the job and returns focus when kept running', async ({ page }) => {
  await connect(page);
  const lines = 1000;
  const name = `operator-cancel-confirm-${test.info().retry}.gcode`;
  const row = await uploadJob(page, name, lines);
  const progress = await startJob(page, row);
  await waitForProgress(progress, lines);
  await expect(row.getByRole('button', { name: `View ${name}` })).toBeVisible();
  const cancel = row.getByRole('button', { name: `Cancel ${name}` });
  await cancel.click();
  const dialog = page.getByRole('dialog', { name: `Cancel ${name}?` });
  await expect(dialog).toContainText('simulator');
  await expect(dialog).toContainText(/cannot be resumed/i);
  await dialog.getByRole('button', { name: 'Keep running' }).click();
  await expect(dialog).toBeHidden();
  await expect(cancel).toBeFocused();
  await expect(row).toContainText(/running/i);
  await cancel.click();
  await dialog.getByRole('button', { name: 'Cancel job' }).click();
  await expect(row).toContainText(/canceled/i, { timeout: 10000 });
});
