const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

test('project position validation identifies the specific field and rule', async ({ page }) => {
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');

  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill('Validation profile');
  await page.locator('#profile-width').fill('300');
  await page.locator('#profile-height').fill('200');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('1');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await page.locator('#project-name').fill('Negative origin');
  await page.locator('#rect-x').fill('-1');
  await page.locator('#rect-y').fill('1.23456');
  const xPosition = page.locator('#rect-x');
  await expect(xPosition).toHaveAttribute('aria-invalid', 'true');
  const errorID = await xPosition.getAttribute('aria-describedby');
  await expect(page.locator(`#${errorID}`)).toHaveText('Position cannot be negative.');
  await expect(page.locator('#rect-y')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#project-validation')).toContainText('Fix 2 fields:');
  await expect(page.locator('#project-validation')).toContainText('Use at most three decimal places.');
});

test('profile feed cross-field validation points to the invalid maximum before submit', async ({ page }) => {
  let profileRequestSent = false;
  page.on('request', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/profiles') profileRequestSent = true;
  });
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill('Invalid feed range');
  await page.locator('#profile-width').fill('300');
  await page.locator('#profile-height').fill('200');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('5000');
  await page.locator('#profile-max-feed').fill('100');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#profile-max-feed')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#profile-max-feed-error')).toHaveText('Maximum feed must be at least the minimum feed.');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-width').fill('');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#profile-width')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#profile-width-error')).toHaveText('Enter a value for this field.');
  expect(profileRequestSent).toBe(false);
});

test('binary uploads get a distinct accessible file error', async ({ page }) => {
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.locator('#job-file').setInputFiles({
    name: 'drawing.png',
    mimeType: 'image/png',
    buffer: Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x00, 0xff]),
  });
  await page.locator('#upload-form button[type="submit"]').click();
  await expect(page.locator('#job-file')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#upload-validation')).toContainText('plain text G-code');
});

test('blank upload name gets an inline error instead of a native validation bubble', async ({ page }) => {
  let uploadRequestSent = false;
  page.on('request', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/jobs') uploadRequestSent = true;
  });
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.locator('#job-file').setInputFiles({ name: 'panel.gcode', mimeType: 'text/plain', buffer: Buffer.from('G21\nG90\nM5\n') });
  await page.locator('#job-name').fill('');
  await page.locator('#upload-form button[type="submit"]').click();
  await expect(page.locator('#job-name')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#job-name-error')).toHaveText('Enter a display name for this program.');
  expect(uploadRequestSent).toBe(false);
});
