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
  await page.getByRole('button', { name: 'Add ellipse' }).click();
  const objectButtons = page.locator('#vector-object-list .vector-select');
  await objectButtons.nth(0).focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#vector-preview-content .vector-path').nth(0)).toHaveClass(/selected-vector-path/);
  await expect(page.locator('#vector-preview-content [role="button"], #vector-preview-content [tabindex]')).toHaveCount(0);
  await objectButtons.nth(0).click();

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
  await page.locator('#vector-rotation').fill('45');

  const handle = page.locator('#vector-preview-content .vector-resize-handle');
  await expect(handle).toBeVisible();
  await expect(handle).toHaveAttribute('aria-hidden', 'true');
  await handle.scrollIntoViewIfNeeded();
  const handleBox = await handle.boundingBox();
  const beforeScale = await shape.getAttribute('d');
  const rotatedLocalDrag = await page.locator('#vector-preview').evaluate(svg => {
    const matrix = svg.getScreenCTM();
    const distance = 10 * 1.45;
    const angle = Math.PI / 4;
    return {
      x: matrix.a * distance * Math.cos(angle) + matrix.c * -distance * Math.sin(angle),
      y: matrix.b * distance * Math.cos(angle) + matrix.d * -distance * Math.sin(angle),
    };
  });
  await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + handleBox.height / 2);
  await page.mouse.down();
  await page.mouse.move(handleBox.x + handleBox.width / 2 + rotatedLocalDrag.x, handleBox.y + handleBox.height / 2 + rotatedLocalDrag.y);
  await page.mouse.up();
  await expect(shape).not.toHaveAttribute('d', beforeScale);
  await expect(page.locator('#vector-scale-x')).toHaveValue('1.667');
  await expect(page.locator('#vector-scale-y')).toHaveValue('1');

  const transformedD = await shape.getAttribute('d');
  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');
  expect(savedPayload.objects[0].transform.translate_x_mm).not.toBe(0);
  expect(savedPayload.objects[0].transform.scale_x).toBeCloseTo(1.667, 3);
  expect(savedPayload.objects[0].transform.scale_y).toBe(1);
  expect(savedPayload.objects[0].transform.rotation_deg).toBe(45);
  await page.getByRole('link', { name: 'Job library' }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator('.workspace')).toBeVisible();
  await expect(page.locator('#designer-panel')).toBeHidden();
  await page.goto('/designer');
  await page.getByRole('button', { name: `Reopen vector project Mouse project ${attempt}` }).click();
  await expect(page.locator('#vector-scale-x')).toHaveValue('1.667');
  await expect(page.locator('#vector-scale-y')).toHaveValue('1');
  await expect(page.locator('#vector-rotation')).toHaveValue('45');
  await expect(page.locator('#vector-translate-x')).toHaveValue(String(savedPayload.objects[0].transform.translate_x_mm));
  await expect(page.locator('#vector-translate-y')).toHaveValue(String(savedPayload.objects[0].transform.translate_y_mm));
  await expect(page.locator('#vector-preview-content .vector-path').first()).toHaveAttribute('d', transformedD);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
