const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';
const manualCapabilities = ['manual_home', 'manual_jog', 'manual_set_zero', 'manual_frame'];

async function connectWithDriver(page, driver) {
  await page.route('**/api/v1/drivers', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify([driver]),
  }));
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-controls')).toContainText('Connected');
  await expect(page.locator('#driver-select')).toHaveValue(driver.id);
}

test('manual jog requires advertised capability, Idle, preparation check, and a second confirmation', async ({ page }) => {
  const driver = { id: 'simulator', busy: false, last_seen: new Date().toISOString(), capabilities: manualCapabilities, controller_state: 'Idle', camera_available: false };
  let record = null;
  let submitted = null;
  let deletes = 0;
  await page.route('**/api/v1/drivers/simulator/manual', async (route) => {
    if (route.request().method() === 'GET') {
      if (!record) return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'manual-control assignment not found' }) });
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(record) });
    }
    if (route.request().method() === 'POST') {
      submitted = route.request().postDataJSON();
      record = { id: 'manual-1', driver_id: driver.id, action: submitted.action, state: 'queued', laser_off: true, max_duration_ms: 10000 };
      driver.busy = true;
      return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify(record) });
    }
    deletes++;
    record = null;
    driver.busy = false;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ state: 'released' }) });
  });
  await connectWithDriver(page, driver);

  const jog = page.getByRole('button', { name: 'Jog', exact: true });
  await expect(jog).toBeDisabled();
  await expect(page.locator('#manual-home')).toBeDisabled();
  await page.locator('#manual-confirmation').check();
  await expect(jog).toBeEnabled();
  await page.locator('#manual-jog-distance').fill('26');
  await jog.click();
  await expect(page.locator('#manual-status')).toContainText('within ±25 mm');
  await expect(page.locator('#action-dialog')).toBeHidden();

  await page.locator('#manual-jog-distance').fill('5');
  await jog.click();
  await expect(page.locator('#action-dialog-title')).toHaveText('Jog machine?');
  await expect(page.locator('#action-dialog-body')).toContainText('command the laser off');
  expect(submitted).toBeNull();
  await page.locator('#action-dialog-confirm').click();
  await expect.poll(() => submitted).toMatchObject({ action: 'jog', confirmed: true, axis: 'X', distance_mm: 5, feed_mm_min: 300 });
  expect(submitted).not.toHaveProperty('gcode');
  await expect(page.locator('#manual-status')).toContainText('Manual control queued');
  await expect(page.locator('#manual-status')).toContainText('waiting for worker');
  await expect(page.locator('#manual-release')).toBeVisible();
  await page.locator('#manual-release').click();
  await expect(page.locator('#action-dialog-title')).toHaveText('Release manual control?');
  await page.locator('#action-dialog-confirm').click();
  await expect.poll(() => deletes).toBe(1);
  await expect(page.locator('#manual-release')).toBeHidden();
});

test('unlock is gated to Alarm and manual actions require the matching capability', async ({ page }) => {
  const driver = { id: 'simulator', busy: false, last_seen: new Date().toISOString(), capabilities: ['manual_unlock', 'manual_home'], controller_state: 'Alarm:1', camera_available: false };
  let submitted = null;
  await page.route('**/api/v1/drivers/simulator/manual', async (route) => {
    if (route.request().method() === 'GET') return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'manual-control assignment not found' }) });
    submitted = route.request().postDataJSON();
    return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ id: 'manual-2', driver_id: driver.id, action: 'unlock', state: 'queued', laser_off: true, max_duration_ms: 5000 }) });
  });
  await connectWithDriver(page, driver);
  await page.locator('#manual-confirmation').check();
  await expect(page.locator('#manual-unlock')).toBeEnabled();
  await expect(page.locator('#manual-home')).toBeDisabled();
  await page.locator('#manual-unlock').click();
  await page.locator('#action-dialog-confirm').click();
  await expect.poll(() => submitted).toMatchObject({ action: 'unlock', confirmed: true });
});
