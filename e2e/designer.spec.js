const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

test('operator saves a declared profile, edits a bounded rectangle, and reviews a queued generated job', async ({ page }) => {
  test.setTimeout(90000);
  const attempt = `${test.info().repeatEachIndex}-${test.info().retry}-${Date.now()}`;
  const profileName = `E2E declared GRBL ${attempt}`;
  const projectName = `E2E panel outline ${attempt}`;
  const startRequests = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && /\/api\/v1\/jobs\/[^/]+\/start$/.test(new URL(request.url()).pathname)) startRequests.push(request);
  });
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');

  await expect(page.getByRole('button', { name: 'New profile' })).toBeVisible();
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.getByLabel('Profile name').fill(profileName);
  await page.getByLabel('Work area width (mm)').fill('300');
  await page.getByLabel('Work area height (mm)').fill('200');
  await page.getByLabel('Maximum spindle value ($30)').fill('1000');
  await expect(page.getByText(/declare \$31=0 and \$32=1/i)).toBeVisible();
  await expect(page.getByLabel('Minimum spindle value ($31)')).toHaveValue('0');
  await page.getByLabel('Minimum feed (mm/min)').fill('1');
  await page.getByLabel('Maximum feed (mm/min)').fill('6000');
  await page.getByLabel('Maximum commanded power (%)').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#designer-status')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await page.locator('#project-name').fill(projectName);
  await page.locator('#rect-x').fill('10');
  await page.locator('#rect-y').fill('20');
  await page.locator('#rect-width').fill('30');
  await page.locator('#rect-height').fill('20');
  await page.locator('#operation-feed').fill('600');
  await page.locator('#operation-power').fill('25');
  await expect(page.getByRole('img', { name: /declared workspace/i })).toBeVisible();

  await page.locator('#rect-width').fill('400');
  await expect(page.locator('#project-validation')).toContainText(/outside the declared workspace/i);
  await expect(page.getByRole('button', { name: 'Save project' })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
  await page.locator('#rect-width').fill('30');
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#designer-status')).toContainText('Project saved');

  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen ${projectName}` }).click();
  await expect(page.locator('#rect-width')).toHaveValue('30');
  await page.locator('#rect-x').fill('11');
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
  await expect(page.locator('#project-save-hint')).toBeVisible();
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#designer-status')).toContainText('Project saved');

  await page.getByRole('button', { name: 'Edit profile' }).click();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
  await expect(page.locator('#profile-save-hint')).toBeVisible();
  await page.getByLabel('Work area width (mm)').fill('290');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#designer-status')).toContainText('Profile saved');
  await expect(page.locator('#stale-profile-warning')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#designer-status')).toContainText('Project saved');
  await expect(page.locator('#stale-profile-warning')).toBeHidden();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeEnabled();
  await page.getByRole('button', { name: 'Edit profile' }).click();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
  await expect(page.locator('#profile-save-hint')).toBeVisible();
  await page.locator('#profile-form').getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.locator('#profile-save-hint')).toBeHidden();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeEnabled();

  await page.locator('#operator-confirmation').check();
  await expect(page.locator('#operator-confirmation')).toBeChecked();
  await page.getByRole('button', { name: 'Generate queued job' }).click();
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  const row = page.locator('#job-list li').filter({ hasText: `${projectName}.gcode` });
  await expect(row).toContainText(/queued/i);
  await expect(page.locator('#program-panel')).toBeVisible();
  await expect(page.locator('#program-code')).toContainText('G0 X11 Y20');
  await expect(page.locator('#program-code')).toContainText('G1 X41 Y20 F600 S250');
  await expect(page.locator('#program-provenance')).toContainText(projectName);
  await expect(page.locator('#program-provenance')).toContainText('commanded power 25%');
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  await expect(row.getByRole('button', { name: 'Start' })).toBeDisabled();
  expect(startRequests).toHaveLength(0);
  await page.locator('#operator-confirmation').check();
  await row.getByRole('button', { name: 'Start' }).click();
  expect(startRequests).toHaveLength(1);
  await expect(row).toContainText(/completed/i, { timeout: 30000 });

  const generatedCode = await page.locator('#program-code').textContent();
  const projectRow = page.locator('#project-list li').filter({ hasText: projectName });
  page.once('dialog', (dialog) => dialog.accept());
  await projectRow.getByRole('button', { name: `Delete project ${projectName}` }).click();
  await expect(projectRow).toHaveCount(0);
  await expect(row).toBeVisible();
  await row.getByRole('button', { name: 'View' }).click();
  await expect(page.locator('#program-code')).toHaveText(generatedCode);
  await expect(page.locator('#program-provenance')).toContainText(projectName);

  page.once('dialog', (dialog) => dialog.accept());
  await page.getByRole('button', { name: 'Delete profile' }).click();
  await expect(page.locator('#designer-status')).toContainText('Profile deleted');
  await expect(row).toBeVisible();

  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('rectangle bounds accept exact three-decimal millimetre edges', async ({ page }) => {
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`Decimal edge ${test.info().repeatEachIndex}-${test.info().retry}`);
  await page.locator('#profile-width').fill('0.3');
  await page.locator('#profile-height').fill('0.3');
  await page.locator('#profile-max-s').fill('50000');
  await page.locator('#profile-min-feed').fill('1');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#designer-status')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await page.locator('#project-name').fill('Decimal edge rectangle');
  await page.locator('#rect-x').fill('0.1');
  await page.locator('#rect-y').fill('0.1');
  await page.locator('#rect-width').fill('0.2');
  await page.locator('#rect-height').fill('0.2');
  await page.locator('#operation-feed').fill('600');
  await page.locator('#operation-power').fill('0.009');
  await expect(page.locator('#project-validation')).toBeHidden();
  await page.locator('#rect-x').fill('0.101');
  await expect(page.locator('#project-validation')).toContainText(/outside the declared workspace/i);
  await page.locator('#rect-x').fill('0.1');
  await expect(page.locator('#project-validation')).toBeHidden();
});

test('open profile and rectangle editors fit tablet and phone viewports', async ({ page }) => {
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`Viewport profile ${test.info().retry}`);
  await page.locator('#profile-width').fill('300');
  await page.locator('#profile-height').fill('200');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('1');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#designer-status')).toContainText('Profile saved');
  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await page.locator('#project-name').fill('Viewport rectangle');
  await page.getByRole('button', { name: 'Edit profile' }).click();
  await expect(page.locator('#profile-form')).toBeVisible();
  await expect(page.locator('#project-form')).toBeVisible();

  for (const width of [1440, 1024, 900, 800, 768, 761, 760, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const fits = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);
    expect(fits, `document width fits at ${width}px`).toBe(true);
    if (width === 800) await page.screenshot({ path: test.info().outputPath('designer-editors-800.png'), fullPage: true });
  }
});
