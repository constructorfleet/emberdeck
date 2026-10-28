const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

test('selected vector object transform is previewed, saved, reopened, and applied to generated geometry', async ({ page }) => {
  test.setTimeout(90000);
  const attempt = `${test.info().repeatEachIndex}-${test.info().retry}-${Date.now()}`;
  const projectName = `E2E transformed vector ${attempt}`;
  let savedPayload;
  page.on('request', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/projects') {
      savedPayload = request.postDataJSON();
    }
  });

  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');

  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`Transform profile ${attempt}`);
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
  const preview = page.locator('#vector-preview-content .vector-path').first();
  const originalPath = await preview.getAttribute('d');
  await page.getByLabel('Translate X (mm)').fill('5');
  await page.getByLabel('Translate Y (mm)').fill('20');
  await page.getByLabel('Scale X').fill('2');
  await page.getByLabel('Scale Y').fill('1');
  await page.getByLabel('Rotation (degrees)').fill('90');
  await expect(preview).not.toHaveAttribute('d', originalPath);
  await expect(page.locator('#vector-validation')).toBeHidden();

  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');
  expect(savedPayload.objects[0].transform).toMatchObject({
    translate_x_mm: 5, translate_y_mm: 20, scale_x: 2, scale_y: 1, rotation_deg: 90,
  });

  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen vector project ${projectName}` }).click();
  await expect(page.getByLabel('Translate X (mm)')).toHaveValue('5');
  await expect(page.getByLabel('Translate Y (mm)')).toHaveValue('20');
  await expect(page.getByLabel('Scale X')).toHaveValue('2');
  await expect(page.getByLabel('Scale Y')).toHaveValue('1');
  await expect(page.getByLabel('Rotation (degrees)')).toHaveValue('90');
  await expect(page.locator('#generate-vector-project')).toBeEnabled();
  await page.locator('#generate-vector-project').click();
  await expect(page.locator('#program-code')).toContainText('X20 Y10');
  await expect(page.locator('#program-code')).toContainText('X20 Y70');
});
