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
  await row.getByRole('button', { name: 'View', exact: true }).click();
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

test('multi-path vector project saves, reopens, generates mixed operations, and dispatches only on explicit Start', async ({ page }) => {
  test.setTimeout(90000);
  const attempt = `${test.info().repeatEachIndex}-${test.info().retry}-${Date.now()}`;
  const projectName = `E2E mixed vector ${attempt}`;
  const startRequests = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && /\/api\/v1\/jobs\/[^/]+\/start$/.test(new URL(request.url()).pathname)) startRequests.push(request);
  });
  await page.goto('/');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`Vector profile ${attempt}`);
  await page.locator('#profile-width').fill('300');
  await page.locator('#profile-height').fill('200');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('1');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#designer-status')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New vector project' }).click();
  await page.locator('#vector-name').fill(projectName);
  await page.getByRole('button', { name: 'Add rectangle' }).click();
  await page.getByRole('button', { name: 'Add ellipse' }).click();
  await page.getByRole('button', { name: 'Add line' }).click();
  await expect(page.locator('#vector-object-list li')).toHaveCount(3);
  await page.getByRole('button', { name: 'Add layer' }).click();
  await expect(page.locator('#vector-operation-list li')).toHaveCount(2);
  await page.getByRole('combobox', { name: 'Operation 2 type' }).selectOption('vector-cut');
  await page.getByLabel('Operation 2 feed (mm/min)').fill('300');
  await page.getByLabel('Operation 2 power (%)').fill('50');
  await page.getByRole('button', { name: '2. ellipse' }).click();
  await page.locator('#vector-object-operation').selectOption({ label: '2. cut' });
  await page.getByRole('button', { name: '3. line' }).click();
  await page.locator('#vector-object-operation').selectOption({ label: '1. engrave' });
  await expect(page.locator('#vector-validation')).toBeHidden();
  await expect(page.locator('#vector-preview-content .vector-path')).toHaveCount(3);
  await expect(page.locator('#generate-vector-project')).toBeDisabled();
  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#designer-status')).toContainText('Vector project saved');

  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen vector ${projectName}` }).click();
  await expect(page.locator('#vector-name')).toHaveValue(projectName);
  await expect(page.locator('#vector-object-list li')).toHaveCount(3);
  await expect(page.getByRole('button', { name: '2. ellipse' })).toBeVisible();
  await expect(page.locator('#vector-operation-list li')).toHaveCount(2);
  await expect(page.locator('#generate-vector-project')).toBeEnabled();
  await page.locator('#vector-width').fill('0');
  await expect(page.locator('#vector-validation')).toBeVisible();
  await expect(page.locator('#generate-vector-project')).toBeDisabled();
  await page.locator('#vector-width').fill('30');
  await expect(page.locator('#vector-validation')).toBeHidden();
  await expect(page.locator('#generate-vector-project')).toBeEnabled();
  for (const width of [1440, 1024, 900, 800, 768, 761, 760, 390]) {
    await page.setViewportSize({ width, height: 900 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `vector editor fits at ${width}px`).toBe(true);
  }
  await page.locator('#generate-vector-project').click();
  expect(startRequests).toHaveLength(0);
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  const row = page.locator('#job-list li').filter({ hasText: `${projectName}.gcode` });
  await expect(row).toContainText(/queued/i);
  await expect(page.locator('#program-code')).toContainText('M4 S0');
  await expect.poll(() => page.locator('#program-code').textContent().then((code) => (code.match(/M4 S0/g) || []).length)).toBe(3);
  await expect(page.locator('#program-code')).toContainText('F300 S500');
  await expect(page.locator('#program-provenance')).toContainText('Geometry snapshot: 3 objects · 3 paths');
  await expect(page.locator('#program-provenance')).toContainText('1. Engrave · 2 objects · feed 600 mm/min · commanded power 25%');
  await expect(page.locator('#program-provenance')).toContainText('2. Cut · 1 object · feed 300 mm/min · commanded power 50%');
  await expect(row.getByRole('button', { name: 'Start' })).toBeDisabled();
  expect(startRequests).toHaveLength(0);

  await page.locator('#operator-confirmation').check();
  await expect(page.locator('#driver-select')).toHaveValue('simulator');
  await row.getByRole('button', { name: 'Start' }).click();
  expect(startRequests).toHaveLength(1);
  await expect(row).toContainText(/completed/i, { timeout: 30000 });

  const pathName = `E2E imported path ${attempt}`;
  const imported = await page.evaluate(async (name) => {
    const profileID = document.querySelector('#profile-select').value;
    const profile = (await (await fetch('/api/v1/profiles', { headers: { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}` } })).json()).find((candidate) => candidate.id === profileID);
    const response = await fetch('/api/v1/projects', {
      method: 'POST',
      headers: { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        schema_version: 2, name, units: 'mm', profile_id: profile.id, profile_revision: profile.revision,
        objects: [{ id: 'imported-path', kind: 'path', operation_id: 'path-engrave', subpaths: [
          { points: [{ x_mm: 10, y_mm: 10 }, { x_mm: 20, y_mm: 10 }, { x_mm: 20, y_mm: 20 }, { x_mm: 10, y_mm: 20 }], closed: true },
          { points: [{ x_mm: 12, y_mm: 12 }, { x_mm: 18, y_mm: 18 }], closed: false },
        ] }],
        operations: [{ id: 'path-engrave', type: 'vector-engrave', feed_mm_min: 600, power_percent: 25 }],
      }),
    });
    return response.json();
  }, pathName);
  expect(imported.objects[0].kind).toBe('path');
  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen vector ${pathName}` }).click();
  await expect(page.getByRole('button', { name: '1. path' })).toBeVisible();
  await page.locator('#vector-width').fill('20');
  await page.locator('#vector-height').fill('20');
  let updatedPathPayload;
  page.on('request', (request) => {
    if (request.method() === 'PUT' && new URL(request.url()).pathname.endsWith(`/projects/${imported.id}`)) updatedPathPayload = request.postDataJSON();
  });
  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#designer-status')).toContainText('Vector project saved');
  expect(updatedPathPayload.objects[0].kind).toBe('path');
  expect(updatedPathPayload.objects[0].subpaths).toHaveLength(2);
  expect(updatedPathPayload.objects[0].subpaths[0].points).toEqual([
    { x_mm: 10, y_mm: 10 }, { x_mm: 30, y_mm: 10 }, { x_mm: 30, y_mm: 30 }, { x_mm: 10, y_mm: 30 },
  ]);
  expect(updatedPathPayload.objects[0].subpaths[1].points).toEqual([{ x_mm: 14, y_mm: 14 }, { x_mm: 26, y_mm: 26 }]);
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
