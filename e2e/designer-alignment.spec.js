const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

test('keyboard multi-selection aligns transformed shapes and persists the new placement', async ({ page }) => {
  test.setTimeout(90000);
  const attempt = `${test.info().repeatEachIndex}-${test.info().retry}-${Date.now()}`;
  const projectName = `E2E alignment ${attempt}`;
  let savedPayload;
  page.on('request', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/projects') savedPayload = request.postDataJSON();
  });

  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`Alignment profile ${attempt}`);
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
  await page.getByRole('button', { name: 'Add rectangle' }).click();
  const alignLeft = page.getByRole('button', { name: 'Align left' });
  await expect(alignLeft).toBeVisible({ timeout: 2000 });
  await expect(alignLeft).toBeDisabled();
  await page.getByRole('button', { name: 'Add ellipse' }).click();
  await expect(alignLeft).toBeDisabled();

  // Rotate the second shape so alignment must use its actual transformed bounds.
  await page.getByRole('button', { name: '2. ellipse' }).click();
  await page.getByLabel('Rotation (degrees)').fill('90');
  await expect(page.locator('#vector-validation')).toBeHidden();

  const checks = page.getByRole('checkbox', { name: /Select shape/ });
  await checks.nth(1).focus();
  await page.keyboard.press('Space');
  await expect(checks.nth(1)).toBeFocused();
  await expect(checks.nth(1)).toHaveCSS('outline-width', '2px');
  await page.keyboard.press('Shift+Tab');
  await page.keyboard.press('Shift+Tab');
  await page.keyboard.press('Shift+Tab');
  await expect(checks.nth(0)).toBeFocused();
  await page.keyboard.press('Space');
  await expect(checks.nth(0)).toBeFocused();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Tab');
  await page.keyboard.press('Tab');
  await expect(checks.nth(1)).toBeFocused();
  await page.keyboard.press('Space');
  await expect(checks.nth(1)).toBeFocused();
  await expect(alignLeft).toBeEnabled();
  await alignLeft.click();
  await expect(page.locator('#vector-align-status')).toContainText('2 shapes aligned left');
  await expect(page.locator('#vector-validation')).toBeHidden();
  await page.getByRole('button', { name: '2. ellipse' }).click();
  await expect(page.getByLabel('Translate X (mm)')).toHaveValue('-10');
  await page.getByRole('button', { name: 'Align horizontal centers' }).click();
  await expect(page.getByLabel('Translate X (mm)')).toHaveValue('-5');
  await page.getByRole('button', { name: 'Align right' }).click();
  await expect(page.getByLabel('Translate X (mm)')).toHaveValue('0');
  await page.getByRole('button', { name: 'Align top' }).click();
  await page.getByRole('button', { name: '1. rectangle' }).click();
  await expect(page.getByLabel('Translate Y (mm)')).toHaveValue('10');
  await page.getByRole('button', { name: 'Align vertical centers' }).click();
  await expect(page.getByLabel('Translate Y (mm)')).toHaveValue('5');
  await page.getByRole('button', { name: 'Align bottom' }).click();
  await expect(page.getByLabel('Translate Y (mm)')).toHaveValue('0');
  await page.getByRole('button', { name: 'Align left' }).click();
  await page.getByRole('button', { name: '2. ellipse' }).click();
  await expect(page.getByLabel('Translate X (mm)')).toHaveValue('-10');

  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');
  expect(savedPayload.objects[0].transform).toBeUndefined();
  expect(savedPayload.objects[1].transform.translate_x_mm).toBe(-10);
  expect(savedPayload.objects[1].transform.rotation_deg).toBe(90);

  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen vector ${projectName}` }).click();
  await page.getByRole('button', { name: '2. ellipse' }).click();
  await expect(page.getByLabel('Translate X (mm)')).toHaveValue('-10');
  await expect(page.getByLabel('Rotation (degrees)')).toHaveValue('90');
  await page.locator('#generate-vector-project').click();
  await expect(page.locator('#program-code')).toContainText('X20 Y40');

  await page.getByRole('button', { name: 'Add ellipse' }).click();
  await page.getByLabel('Translate X (mm)').fill('500');
  await expect(page.locator('#vector-validation')).toContainText(/outside the declared workspace/i);
  const reopenedChecks = page.getByRole('checkbox', { name: /Select shape/ });
  await reopenedChecks.nth(2).uncheck();
  await reopenedChecks.nth(0).check();
  await reopenedChecks.nth(1).check();
  await page.getByRole('button', { name: 'Align right' }).click();
  await expect(page.locator('#vector-validation')).toContainText(/outside the declared workspace/i);
  await expect(page.getByRole('button', { name: 'Save vector project' })).toBeDisabled();
});
