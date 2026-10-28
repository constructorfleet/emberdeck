const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

async function connect(page) {
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-controls')).toContainText('Connected');
  await expect(page.locator('#driver-select')).toHaveValue('simulator');
}

async function uploadJob(page, name, lineCount) {
  const program = `${Array.from({ length: lineCount }, () => 'G1 X1 F100').join('\n')}\n`;
  await page.locator('#job-file').setInputFiles({ name, mimeType: 'text/plain', buffer: Buffer.from(program) });
  await page.locator('#job-name').fill(name);
  await page.getByRole('button', { name: /Add to library/ }).click();
  const row = page.locator('#job-list li').filter({ hasText: name });
  await expect(row).toContainText(/queued/i);
  return row;
}

async function startJob(page, row) {
  await page.locator('#operator-confirmation').check();
  const boundsOverride = row.getByRole('checkbox', { name: /Override .* bounds review/ });
  if (await boundsOverride.count()) await boundsOverride.check();
  await row.getByRole('button', { name: 'Start' }).click();
}

test('job list shows run times and the job view shows the event history', async ({ page }) => {
  await connect(page);
  const name = `history-${test.info().retry}.gcode`;
  const row = await uploadJob(page, name, 3);
  await expect(row.locator('.job-times')).toHaveText('Not started');

  await startJob(page, row);
  await expect(row).toContainText(/completed/i, { timeout: 30000 });
  const times = row.locator('.job-times');
  await expect(times).toContainText('Started');
  await expect(times).toContainText('Ended');
  await expect(times).toContainText(/Duration \d/);
  await expect(times.locator('time')).toHaveCount(2);

  await row.getByRole('button', { name: 'View', exact: true }).click();
  const history = page.getByRole('list', { name: 'Job event history' });
  await expect(history).toBeVisible();
  const entries = history.getByRole('listitem');
  await expect(entries).toHaveCount(3);
  await expect(entries.nth(0)).toContainText('Uploaded');
  await expect(entries.nth(0)).toContainText('queued');
  await expect(entries.nth(0)).toContainText('operator');
  await expect(entries.nth(1)).toContainText('Dispatched');
  await expect(entries.nth(1)).toContainText('queued → running');
  await expect(entries.nth(1)).toContainText('driver simulator');
  await expect(entries.nth(1)).toContainText(/lease [0-9a-f]{16}/);
  await expect(entries.nth(2)).toContainText('Worker report');
  await expect(entries.nth(2)).toContainText('running → completed');
  await expect(entries.nth(2)).toContainText('worker');

  // The view shows short opaque references, never a 64-hex lease token.
  await expect(page.locator('#program-history')).not.toContainText(/[0-9a-f]{64}/);

  // History remains after deletion, and the open view records it.
  page.once('dialog', (dialog) => dialog.accept());
  const id = await page.evaluate(async ({ key, jobName }) => {
    const list = await (await fetch(`/api/v1/jobs?name=${encodeURIComponent(jobName)}`, { headers: { Authorization: `Bearer ${key}` } })).json();
    return list.jobs[0].id;
  }, { key: apiKey, jobName: name });
  await row.getByRole('button', { name: /Delete/ }).click();
  await expect(page.locator('#notice')).toHaveText('Program deleted.');
  const deleted = await page.evaluate(async ({ key, jobID }) => {
    const response = await fetch(`/api/v1/jobs/${jobID}/events`, { headers: { Authorization: `Bearer ${key}` } });
    return (await response.json()).events.map((event) => event.to);
  }, { key: apiKey, jobID: id });
  expect(deleted).toEqual(['queued', 'running', 'completed', 'deleted']);
});

test('event history fits a narrow screen', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await connect(page);
  const name = `history-narrow-${test.info().retry}.gcode`;
  const row = await uploadJob(page, name, 1);
  await row.getByRole('button', { name: 'View', exact: true }).click();
  const history = page.getByRole('list', { name: 'Job event history' });
  await expect(history.getByRole('listitem')).toHaveCount(1);
  await expect(history.getByRole('listitem').first()).toContainText('Uploaded');
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
});
