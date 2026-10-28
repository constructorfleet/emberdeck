const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

test('rectangle preview keeps readable labels and shows an out-of-bounds rectangle', async ({ page }) => {
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');

  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`Preview profile ${Date.now()}`);
  await page.locator('#profile-width').fill('400');
  await page.locator('#profile-height').fill('300');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('1');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await page.locator('#project-name').fill('Out-of-bounds preview');
  await page.locator('#rect-x').fill('390');
  await page.locator('#rect-y').fill('20');
  await page.locator('#rect-width').fill('30');
  await page.locator('#rect-height').fill('20');
  await page.locator('#operation-feed').fill('600');
  await page.locator('#operation-power').fill('25');
  await expect(page.locator('#project-validation')).toContainText(/outside the declared workspace/i);

  const preview = page.locator('#project-preview');
  const workspace = preview.locator('.workspace-shape');
  const rectangle = preview.locator('.engrave-path.out-of-bounds');
  await expect(rectangle).toHaveCount(1);
  const warningStyle = await rectangle.evaluate((node) => ({
    stroke: getComputedStyle(node).stroke,
    warning: getComputedStyle(document.querySelector('#preview-approach-label')).color,
  }));
  expect(warningStyle.stroke).toBe(warningStyle.warning);
  const geometry = await page.evaluate(() => {
    const workspace = document.querySelector('#preview-content .workspace-shape').getBoundingClientRect();
    const rectangle = document.querySelector('#preview-content .out-of-bounds').getBoundingClientRect();
    const svg = document.querySelector('#project-preview').getBoundingClientRect();
    return { overrun: rectangle.right - workspace.right, clipped: rectangle.right > svg.right };
  });
  expect(geometry.overrun).toBeGreaterThan(0);
  expect(geometry.clipped).toBe(false);

  await page.locator('#rect-x').fill('-5');
  await expect(page.locator('#project-validation')).toContainText(/position must be nonnegative/i);
  await expect(rectangle).toHaveCount(1);
  const negativeGeometry = await page.evaluate(() => {
    const workspace = document.querySelector('#preview-content .workspace-shape').getBoundingClientRect();
    const rectangle = document.querySelector('#preview-content .out-of-bounds').getBoundingClientRect();
    const svg = document.querySelector('#project-preview').getBoundingClientRect();
    return { overrun: workspace.left - rectangle.left, clipped: rectangle.left < svg.left };
  });
  expect(negativeGeometry.overrun).toBeGreaterThan(0);
  expect(negativeGeometry.clipped).toBe(false);

  for (const width of [360, 375, 390, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    for (const selector of ['#preview-workspace-label', '#preview-approach-label']) {
      const label = page.locator(selector);
      await expect(label).toBeVisible();
      const fontSize = await label.evaluate((node) => Number.parseFloat(getComputedStyle(node).fontSize));
      expect(fontSize).toBeGreaterThanOrEqual(11);
    }
  }
});
