const { test, expect } = require('@playwright/test');

const openBaseURL = process.env.EMBERDECK_E2E_OPEN_BASE_URL;

test('a server started without EMBERDECK_API_KEY connects without a key', async ({ page }) => {
  test.skip(!openBaseURL, 'open-auth server was not started');
  await page.goto(`${openBaseURL}/`);
  await expect(page.locator('#connection-controls')).toContainText('Connected');
  await expect(page.locator('#driver-select')).toHaveValue('simulator');
  await expect(page.locator('#auth-form')).toBeHidden();
  await expect(page.locator('#change-key')).toBeHidden();
  await expect(page.locator('#disconnect')).toBeHidden();
  expect(await page.evaluate(() => localStorage.getItem('emberdeck-api-key'))).toBeNull();
});
