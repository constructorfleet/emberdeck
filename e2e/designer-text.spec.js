const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

test('editable monoline text saves, reopens, generates engraving geometry, and dispatches only on explicit Start', async ({ page }) => {
  test.setTimeout(90000);
  const attempt = `${test.info().repeatEachIndex}-${test.info().retry}-${Date.now()}`;
  const projectName = `E2E monoline text ${attempt}`;
  const startRequests = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && /\/api\/v1\/jobs\/[^/]+\/start$/.test(new URL(request.url()).pathname)) startRequests.push(request);
  });
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`Text profile ${attempt}`);
  await page.locator('#profile-width').fill('300');
  await page.locator('#profile-height').fill('200');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('1');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New vector project' }).click();
  await page.locator('#vector-name').fill(projectName);
  await page.getByRole('button', { name: 'Add text' }).click();
  await expect(page.locator('#vector-text-content')).toHaveValue('EMBER');
  const punctuationGeometry = page.waitForResponse((response) => response.url().includes('/api/v1/designer/text-geometry') && response.request().method() === 'POST');
  await page.locator('#vector-text-content').fill('.');
  await punctuationGeometry;
  await expect(page.locator('#vector-x')).toHaveValue('10');
  await page.locator('#vector-text-content').fill('Q7');
  await page.locator('#vector-text-size').fill('7');
  await expect(page.locator('#vector-validation')).toBeHidden();
  await expect(page.locator('#vector-preview-content .vector-path')).toHaveCount(1);
  await expect.poll(() => page.locator('#vector-preview-content .vector-path').getAttribute('d').then((path) => (path.match(/M /g) || []).length)).toBe(3);
  await page.locator('#vector-text-content').fill('unsupported 🙂');
  await expect(page.locator('#vector-text-error')).toBeVisible();
  await expect(page.locator('#vector-text-error')).toContainText('Use A–Z');
  await expect(page.getByRole('button', { name: 'Save vector project' })).toBeDisabled();
  await page.locator('#vector-text-content').fill('Q7');
  await expect(page.locator('#vector-text-error')).toBeHidden();
  await expect(page.locator('#vector-validation')).toBeHidden();
  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');

  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen vector ${projectName}` }).click();
  await expect(page.locator('#vector-text-content')).toHaveValue('Q7');
  await expect(page.locator('#vector-text-size')).toHaveValue('7');
  await expect(page.locator('#vector-preview-content .vector-path')).toHaveCount(1);
  await expect.poll(() => page.locator('#vector-preview-content .vector-path').getAttribute('d').then((path) => (path.match(/M /g) || []).length)).toBe(3);
  await expect(page.locator('#generate-vector-project')).toBeEnabled();
  await page.locator('#generate-vector-project').click();
  expect(startRequests).toHaveLength(0);
  await page.getByRole('link', { name: 'Job library' }).click();
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  const row = page.locator('#job-list li').filter({ hasText: `${projectName}.gcode` });
  await expect(row).toContainText(/queued/i);
  await row.getByRole('button', { name: /^View / }).click();
  await expect(page.locator('#program-code')).toContainText('G0 X11 Y10\nM4 S0\nG1 X10 Y11 F600 S250');
  await expect(row.getByRole('button', { name: /^Start / })).toBeDisabled();
  expect(startRequests).toHaveLength(0);
  await page.locator('#operator-confirmation').check();
  await row.getByRole('button', { name: /^Start / }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Start on simulator' }).click();
  await expect.poll(() => startRequests.length).toBe(1);
  await expect(row).toContainText(/completed/i, { timeout: 30000 });
});

test('invalid text stays blocked when another text object is edited', async ({ page }) => {
  test.setTimeout(30000);
  const attempt = `${test.info().retry}-${Date.now()}`;
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`Text validation profile ${attempt}`);
  await page.locator('#profile-width').fill('300');
  await page.locator('#profile-height').fill('200');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('1');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New vector project' }).click();
  await page.locator('#vector-name').fill(`Two text validation ${attempt}`);
  await page.getByRole('button', { name: 'Add text' }).click();
  await page.locator('#vector-text-content').fill('unsupported 🙂');
  await expect(page.locator('#vector-text-error')).toContainText('Use A–Z');
  await expect(page.getByRole('button', { name: 'Save vector project' })).toBeDisabled();

  await page.getByRole('button', { name: 'Add text' }).click();
  await expect(page.locator('#vector-text-content')).toHaveValue('EMBER');
  await expect(page.locator('#vector-text-error')).toBeHidden();
  await expect(page.locator('#vector-validation')).toContainText('Use A–Z');
  await expect(page.getByRole('button', { name: 'Save vector project' })).toBeDisabled();

  await page.getByRole('button', { name: '1. text' }).click();
  await expect(page.locator('#vector-text-error')).toContainText('Use A–Z');
  await page.locator('#vector-text-content').fill('ALPHA');
  await expect(page.locator('#vector-text-error')).toBeHidden();
  await expect(page.locator('#vector-validation')).toBeHidden();
  await expect(page.getByRole('button', { name: 'Save vector project' })).toBeEnabled();

  await page.route('**/api/v1/designer/text-geometry', async (route) => {
    if (route.request().postDataJSON().content === 'ALPHA') await new Promise((resolve) => setTimeout(resolve, 1000));
    await route.continue();
  });
  const alphaResponse = page.waitForResponse((response) => response.request().postDataJSON()?.content === 'ALPHA');
  await page.locator('#vector-text-content').fill('ALPHA');
  await page.getByRole('button', { name: '2. text' }).click();
  const bravoResponse = page.waitForResponse((response) => response.request().postDataJSON()?.content === 'BRAVO');
  await page.locator('#vector-text-content').fill('BRAVO');
  await bravoResponse;
  await expect(page.locator('#vector-validation')).toContainText('Text stroke geometry is still being generated.');
  await expect(page.getByRole('button', { name: 'Save vector project' })).toBeDisabled();
  await alphaResponse;
  await expect(page.locator('#vector-validation')).toBeHidden();
  await expect(page.getByRole('button', { name: 'Save vector project' })).toBeEnabled();
});
