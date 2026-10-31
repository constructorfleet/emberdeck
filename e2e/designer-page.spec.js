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

test('designer only shows shape inputs for a selected shape and guides an empty project', async ({ page }) => {
  const attempt = `${test.info().repeatEachIndex}-${Date.now()}`;
  await page.goto('/designer');
  await connect(page);
  await createProfile(page, `Empty designer profile ${attempt}`);
  await page.getByRole('button', { name: 'New vector project' }).click();

  await expect(page.locator('#vector-inspector-empty')).toBeVisible();
  await expect(page.locator('#vector-inspector-empty')).toContainText(/select a shape/i);
  await expect(page.locator('#vector-object-empty')).toBeVisible();
  await expect(page.locator('#vector-shape-count')).toHaveText('0');
  await expect(page.locator('#vector-geometry')).toBeHidden();
  await expect(page.locator('#vector-transform')).toBeHidden();
  await expect(page.locator('#vector-text-fields')).toBeHidden();
  await expect(page.locator('#vector-validation')).toBeHidden();

  await page.getByRole('button', { name: 'Add rectangle' }).click();
  await expect(page.locator('#vector-shape-count')).toHaveText('1');
  await expect(page.locator('#vector-object-empty')).toBeHidden();
  await expect(page.locator('#vector-inspector-empty')).toBeHidden();
  await expect(page.locator('#vector-geometry')).toBeVisible();
  await expect(page.locator('#vector-transform')).toBeVisible();
  await expect(page.locator('#vector-text-fields')).toBeHidden();
  await expect(page.locator('#vector-object-list li')).toContainText(/mm/);

  await page.getByRole('button', { name: 'Add text' }).click();
  await expect(page.locator('#vector-shape-count')).toHaveText('2');
  await expect(page.locator('#vector-text-fields')).toBeVisible();
  await expect(page.locator('label:has(#vector-width)')).toBeHidden();
  await expect(page.locator('label:has(#vector-height)')).toBeHidden();

  await page.getByRole('button', { name: '1. Rectangle' }).click();
  await expect(page.locator('#vector-text-fields')).toBeHidden();
  await expect(page.locator('label:has(#vector-width)')).toBeVisible();
  await expect(page.locator('#vector-inspector-summary')).toContainText(/rectangle/i);

  await expect(page.locator('#vector-operation-list .layer-feed')).toContainText(/feed mm\/min/i);
  await expect(page.locator('#vector-operation-list .layer-power')).toContainText(/power %/i);
  await expect(page.locator('#vector-operation-list .layer-type')).toContainText(/type/i);
});

test('new projects start with work-area-sized shapes and profile-valid operation defaults', async ({ page }) => {
  const attempt = `${test.info().repeatEachIndex}-${Date.now()}`;
  await page.goto('/designer');
  await connect(page);
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`Sized defaults profile ${attempt}`);
  await page.locator('#profile-width').fill('300');
  await page.locator('#profile-height').fill('200');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('1200');
  await page.locator('#profile-max-feed').fill('1800');
  await page.locator('#profile-max-power').fill('60');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await expect(page.locator('#rect-x')).toHaveValue('90');
  await expect(page.locator('#rect-y')).toHaveValue('60');
  await expect(page.locator('#rect-width')).toHaveValue('120');
  await expect(page.locator('#rect-height')).toHaveValue('80');
  await expect(page.locator('#operation-feed')).toHaveValue('1200');
  await expect(page.locator('#operation-power')).toHaveValue('25');
  await expect(page.locator('#project-validation')).toBeHidden();
  await page.getByRole('button', { name: 'Close editor' }).click();

  await page.getByRole('button', { name: 'New vector project' }).click();
  await expect(page.getByLabel('Operation 1 feed (mm/min)')).toHaveValue('1200');
  await expect(page.getByLabel('Operation 1 power (%)')).toHaveValue('25');
  await expect(page.locator('#vector-validation')).toBeHidden();
  await page.getByRole('button', { name: 'Add rectangle' }).click();
  await expect(page.locator('#vector-x')).toHaveValue('112.5');
  await expect(page.locator('#vector-y')).toHaveValue('75');
  await expect(page.locator('#vector-width')).toHaveValue('75');
  await expect(page.locator('#vector-height')).toHaveValue('50');
  await expect(page.locator('#vector-validation')).toBeHidden();
});

test('designer undo and redo restore shapes, text geometry, and operation layers', async ({ page }) => {
  const attempt = `${test.info().repeatEachIndex}-${Date.now()}`;
  await page.goto('/designer');
  await connect(page);
  await createProfile(page, `History profile ${attempt}`);
  await page.getByRole('button', { name: 'New vector project' }).click();

  const undo = page.getByRole('button', { name: 'Undo' });
  const redo = page.getByRole('button', { name: 'Redo' });
  await expect(undo).toBeDisabled();
  await expect(redo).toBeDisabled();

  await page.getByRole('button', { name: 'Add rectangle' }).click();
  await expect(undo).toBeEnabled();
  await expect(redo).toBeDisabled();

  await page.getByRole('button', { name: 'Add text' }).click();
  await expect(page.locator('#vector-shape-count')).toHaveText('2');
  await expect.poll(() => page.locator('#vector-preview-content .vector-path').count()).toBe(2);

  await undo.click();
  await expect(page.locator('#vector-shape-count')).toHaveText('1');
  await expect(page.locator('#vector-text-fields')).toBeHidden();
  await expect(undo).toBeEnabled();

  await undo.click();
  await expect(page.locator('#vector-shape-count')).toHaveText('0');
  await expect(page.locator('#vector-inspector-empty')).toBeVisible();
  await expect(undo).toBeDisabled();
  await expect(redo).toBeEnabled();

  await redo.click();
  await expect(page.locator('#vector-shape-count')).toHaveText('1');
  await redo.click();
  await expect(page.locator('#vector-shape-count')).toHaveText('2');
  await expect(page.locator('#vector-text-fields')).toBeVisible();
  await expect(page.locator('#vector-text-content')).toHaveValue('EMBER');
  await expect.poll(() => page.locator('#vector-preview-content .vector-path').count()).toBe(2);

  await page.getByRole('button', { name: '2. Text' }).focus();
  await page.keyboard.press('Control+z');
  await expect(page.locator('#vector-shape-count')).toHaveText('1');
  await page.keyboard.press('Control+Shift+z');
  await expect(page.locator('#vector-shape-count')).toHaveText('2');

  await page.locator('#vector-translate-x').fill('5');
  await undo.click();
  await expect(page.locator('#vector-translate-x')).toHaveValue('0');

  await page.getByLabel('Operation 1 feed (mm/min)').fill('300');
  await undo.click();
  await expect(page.getByLabel('Operation 1 feed (mm/min)')).toHaveValue('600');

  await page.locator('#vector-name').fill('Renamed design');
  await undo.click();
  await expect(page.locator('#vector-name')).toHaveValue('Untitled vector design');

  await page.getByRole('button', { name: 'Add layer' }).click();
  await expect(page.locator('#vector-operation-list li')).toHaveCount(2);
  await undo.click();
  await expect(page.locator('#vector-operation-list li')).toHaveCount(1);
  await redo.click();
  await expect(page.locator('#vector-operation-list li')).toHaveCount(2);
});

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
  await shape.scrollIntoViewIfNeeded();
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
  const shapeWidthMm = Number(await page.locator('#vector-width').inputValue());
  const rotatedLocalDrag = await page.locator('#vector-preview').evaluate((svg, widthMm) => {
    const matrix = svg.getScreenCTM();
    const distance = (widthMm / 3) * 1.45;
    const angle = Math.PI / 4;
    return {
      x: matrix.a * distance * Math.cos(angle) + matrix.c * -distance * Math.sin(angle),
      y: matrix.b * distance * Math.cos(angle) + matrix.d * -distance * Math.sin(angle),
    };
  }, shapeWidthMm);
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
