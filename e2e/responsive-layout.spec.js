const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

async function connect(page) {
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
}

test('mobile library stacks the driver label and keeps its header description visible', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto('/');
  await connect(page);

  const driverLabel = page.getByLabel('Connected driver').locator('xpath=preceding-sibling::label[1]');
  const driverSelect = page.getByLabel('Connected driver');
  const labelBox = await driverLabel.boundingBox();
  const selectBox = await driverSelect.boundingBox();
  expect(labelBox.y + labelBox.height).toBeLessThanOrEqual(selectBox.y + 1);
  expect(selectBox.width).toBeGreaterThanOrEqual(240);
  await expect(page.getByText('LASER WORKSPACE', { exact: true })).toBeVisible();
  const headerLabelBox = await page.getByText('LASER WORKSPACE', { exact: true }).boundingBox();
  expect(headerLabelBox.x + headerLabelBox.width).toBeLessThanOrEqual(375);
});

test('library and designer section numbers are sequential with a shared header action slot', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('.auth .section-title .eyebrow')).toHaveText('01 / ACCESS');
  await expect(page.locator('.upload-panel .section-title .eyebrow')).toHaveText('02 / INTAKE');
  await expect(page.locator('.jobs-panel .section-title .eyebrow')).toHaveText('03 / LIBRARY');
  await expect(page.locator('.upload-panel .step-number')).toHaveCount(0);

  for (const selector of ['.auth', '#designer-panel', '.upload-panel', '.jobs-panel', '#program-panel']) {
    await expect(page.locator(`${selector} > .section-title > .section-actions`)).toHaveCount(1);
  }

  await page.goto('/designer');
  await expect(page.locator('.auth .section-title .eyebrow')).toHaveText('01 / ACCESS');
  await expect(page.locator('#designer-panel .section-title .eyebrow')).toHaveText('02 / DESIGN SOURCE');
});

test('job library uses a wider two-column layout on large screens', async ({ page }) => {
  await page.setViewportSize({ width: 1920, height: 1080 });
  await page.goto('/');
  const mainBox = await page.locator('main').boundingBox();
  expect(mainBox.width).toBeGreaterThanOrEqual(1500);
  const columns = await page.locator('.workspace').evaluate((element) => getComputedStyle(element).gridTemplateColumns.split(' ').length);
  expect(columns).toBe(2);
});
