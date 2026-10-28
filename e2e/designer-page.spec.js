const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

async function connect(page) {
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
}

async function createProfile(page, name) {
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(name);
  await page.locator('#profile-width').fill('300');
  await page.locator('#profile-height').fill('200');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('1');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');
}

test('designer has its own page and supports selecting, dragging, and scaling a shape on the workspace', async ({ page }) => {
  const attempt = `${test.info().repeatEachIndex}-${Date.now()}`;
  let savedPayload;
  page.on('request', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/projects') savedPayload = request.postDataJSON();
  });

  await page.goto('/designer');
  await expect(page).toHaveTitle(/Designer/);
  await expect(page.getByRole('heading', { level: 1, name: /Designer/ })).toBeVisible();
  await expect(page.locator('.workspace')).toBeHidden();
  await expect(page.getByRole('link', { name: 'Job library' })).toBeVisible();
  await connect(page);
  await createProfile(page, `Mouse profile ${attempt}`);
  await page.getByRole('button', { name: 'New vector project' }).click();
  await page.locator('#vector-name').fill(`Mouse project ${attempt}`);
  await page.getByRole('button', { name: 'Add rectangle' }).click();

  const shape = page.locator('#vector-preview-content .vector-path').first();
  const originalD = await shape.getAttribute('d');
  const start = await shape.evaluate(element => {
    const point = element.getPointAtLength(0);
    const screen = new DOMPoint(point.x, point.y).matrixTransform(element.ownerSVGElement.getScreenCTM());
    return { x: screen.x, y: screen.y };
  });
  await page.mouse.move(start.x, start.y);
  await page.mouse.down();
  await page.mouse.move(start.x + 24, start.y - 12);
  await page.mouse.up();
  await expect(shape).toHaveClass(/selected-vector-path/);
  await expect(shape).not.toHaveAttribute('d', originalD);
  await expect(page.locator('#vector-save-hint')).toBeVisible();

  const handle = page.locator('#vector-preview-content .vector-resize-handle');
  await expect(handle).toBeVisible();
  const handleBox = await handle.boundingBox();
  const beforeScale = await shape.getAttribute('d');
  await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + handleBox.height / 2);
  await page.mouse.down();
  await page.mouse.move(handleBox.x + handleBox.width / 2 + 30, handleBox.y + handleBox.height / 2 + 20);
  await page.mouse.up();
  await expect(shape).not.toHaveAttribute('d', beforeScale);

  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');
  expect(savedPayload.objects[0].transform.translate_x_mm).not.toBe(0);
  expect(savedPayload.objects[0].transform.scale_x).not.toBe(1);
  await page.getByRole('link', { name: 'Job library' }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator('.workspace')).toBeVisible();
  await expect(page.locator('#designer-panel')).toBeHidden();
  await page.goto('/designer');
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
