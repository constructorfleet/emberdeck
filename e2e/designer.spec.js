const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

test('saved project actions keep keyboard focus through two job poll cycles', async ({ page }) => {
  const attempt = `${test.info().repeatEachIndex}-${test.info().retry}-${Date.now()}`;
  const profileName = `E2E focus profile ${attempt}`;
  const projectName = `E2E focus project ${attempt}`;
  let jobPolls = 0;
  let designerRefreshes = 0;
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (request.method() === 'GET' && url.pathname === '/api/v1/jobs' && url.search) jobPolls++;
    if (request.method() === 'GET' && url.pathname === '/api/v1/profiles') designerRefreshes++;
  });
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');

  await page.getByRole('button', { name: 'New profile' }).click();
  await page.getByLabel('Profile name').fill(profileName);
  await page.getByLabel('Work area width (mm)').fill('300');
  await page.getByLabel('Work area height (mm)').fill('200');
  await page.getByLabel('Maximum spindle value ($30)').fill('1000');
  await page.getByLabel('Minimum feed (mm/min)').fill('1');
  await page.getByLabel('Maximum feed (mm/min)').fill('6000');
  await page.getByLabel('Maximum commanded power (%)').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await page.locator('#project-name').fill(projectName);
  await page.locator('#rect-x').fill('10');
  await page.locator('#rect-y').fill('20');
  await page.locator('#rect-width').fill('30');
  await page.locator('#rect-height').fill('20');
  await page.locator('#operation-feed').fill('600');
  await page.locator('#operation-power').fill('25');
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Project saved');

  const reopen = page.getByRole('button', { name: `Reopen project ${projectName}` });
  await page.evaluate((name) => {
    const row = Array.from(document.querySelectorAll('#project-list li')).find((candidate) => candidate.textContent.includes(name));
    window.__savedProjectRow = row;
    window.__savedProjectAction = row.querySelector('[data-project-action="reopen"]');
  }, projectName);
  await reopen.focus();
  await expect(reopen).toBeFocused();
  const priorRefreshCount = designerRefreshes;
  await page.evaluate(() => document.querySelector('#refresh').click());
  await expect.poll(() => designerRefreshes).toBeGreaterThan(priorRefreshCount);
  expect(await page.evaluate((name) => {
    const row = Array.from(document.querySelectorAll('#project-list li')).find((candidate) => candidate.textContent.includes(name));
    return row === window.__savedProjectRow && row.querySelector('[data-project-action="reopen"]') === window.__savedProjectAction;
  }, projectName)).toBe(true);
  await expect(reopen).toBeFocused();
  await page.waitForTimeout(4300);
  expect(jobPolls).toBeGreaterThanOrEqual(3);
  await expect(reopen).toBeFocused();
});

test('concurrent editors retain local work, reject a racing stale save, and recover on tab return', async ({ browser }) => {
  test.setTimeout(90000);
  const attempt = `${test.info().repeatEachIndex}-${Date.now()}`;
  const profileName = `E2E shared profile ${attempt}`;
  const projectName = `E2E shared project ${attempt}`;
  const contextA = await browser.newContext();
  const contextB = await browser.newContext();
  const pageA = await contextA.newPage();
  const pageB = await contextB.newPage();
  try {
    await pageA.goto('/designer');
    await pageA.locator('#api-key').fill(apiKey);
    await pageA.getByRole('button', { name: /Connect/ }).click();
    await expect(pageA.locator('#connection-state')).toHaveText('Connected · library synced');
    await pageA.getByRole('button', { name: 'New profile' }).click();
    await pageA.getByLabel('Profile name').fill(profileName);
    await pageA.getByLabel('Work area width (mm)').fill('300');
    await pageA.getByLabel('Work area height (mm)').fill('200');
    await pageA.getByLabel('Maximum spindle value ($30)').fill('1000');
    await pageA.getByLabel('Minimum feed (mm/min)').fill('1');
    await pageA.getByLabel('Maximum feed (mm/min)').fill('6000');
    await pageA.getByLabel('Maximum commanded power (%)').fill('100');
    await pageA.getByRole('button', { name: 'Save profile' }).click();
    await expect(pageA.locator('#notice .notice-message')).toContainText('Profile saved');
    await pageA.getByRole('button', { name: 'New rectangle project' }).click();
    await pageA.locator('#project-name').fill(projectName);
    await pageA.locator('#rect-x').fill('10');
    await pageA.getByRole('button', { name: 'Save project' }).click();
    await expect(pageA.locator('#notice .notice-message')).toContainText('Project saved');
    const rowA = pageA.locator('#project-list li').filter({ hasText: projectName });
    const projectID = await rowA.getByRole('button', { name: `Reopen project ${projectName}` }).getAttribute('data-project-id');
    await pageA.locator('#rect-x').fill('11');

    await pageB.goto('/designer');
    await pageB.locator('#api-key').fill(apiKey);
    await pageB.getByRole('button', { name: /Connect/ }).click();
    await expect(pageB.locator('#connection-state')).toHaveText('Connected · library synced');
    await pageB.getByRole('button', { name: `Reopen project ${projectName}` }).click();
    await pageB.locator('#rect-x').fill('12');
    await pageB.getByRole('button', { name: 'Save project' }).click();
    await expect(pageB.locator('#notice .notice-message')).toContainText('Project saved');

    await expect(pageA.locator('#project-remote-change')).toBeVisible({ timeout: 15000 });
    await expect(pageA.locator('#project-remote-message')).toContainText(/newer saved revision \(2\).*saved/i);
    await expect(pageA.locator('#project-remote-message')).toContainText('Your unsaved edits remain in this form.');
    await expect(pageA.locator('#rect-x')).toHaveValue('11');
    await expect(pageA.getByRole('button', { name: 'Save project' })).toBeDisabled();
    const discardPrompt = new Promise((resolve) => pageA.once('dialog', async (dialog) => { await dialog.dismiss(); resolve(dialog.message()); }));
    await pageA.getByRole('button', { name: 'Reload latest' }).click();
    expect(await discardPrompt).toBe('Discard unsaved changes?');
    await expect(pageA.locator('#rect-x')).toHaveValue('11');

    await pageA.getByRole('button', { name: 'Continue as separate project' }).click();
    await expect(pageA.locator('#project-form-title')).toContainText('New copy of');
    await expect(pageA.locator('#rect-x')).toHaveValue('11');
    await expect(pageA.locator('#project-save-hint')).toContainText('unsaved edits');
    const copyDiscardPrompt = new Promise((resolve) => pageA.once('dialog', async (dialog) => { await dialog.dismiss(); resolve(dialog.message()); }));
    await pageA.getByRole('button', { name: 'Close editor' }).click();
    expect(await copyDiscardPrompt).toBe('Discard unsaved changes?');
    await expect(pageA.locator('#rect-x')).toHaveValue('11');
    await pageA.locator('#project-name').fill(`${projectName} copy`);
    await pageA.getByRole('button', { name: 'Save project' }).click();
    await expect(pageA.locator('#notice .notice-message')).toContainText('Project saved');
    await expect(pageA.locator('#project-list li').filter({ hasText: `${projectName} copy` })).toBeVisible();

    await pageA.getByRole('button', { name: 'Close editor' }).click();
    await pageA.getByRole('button', { name: `Reopen project ${projectName}`, exact: true }).click();
    await pageA.locator('#rect-x').fill('13');
    await pageA.route(`**/api/v1/projects/${projectID}`, async (route) => {
      if (route.request().method() === 'PUT') {
        await pageB.locator('#rect-x').fill('14');
        await pageB.getByRole('button', { name: 'Save project' }).click();
        await expect(pageB.locator('#notice .notice-message')).toContainText('Project saved');
      }
      await route.continue();
    });
    await pageA.getByRole('button', { name: 'Save project' }).click();
    await expect(pageA.locator('#notice .notice-message')).toContainText('A newer project revision was saved.');
    await expect(pageA.locator('#project-remote-change')).toBeVisible();
    await expect(pageA.locator('#rect-x')).toHaveValue('13');

    await pageB.locator('#rect-x').fill('15');
    await pageB.getByRole('button', { name: 'Save project' }).click();
    await expect(pageB.locator('#notice .notice-message')).toContainText('Project saved');
    await pageA.unroute(`**/api/v1/projects/${projectID}`);
    await pageB.bringToFront();
    await pageA.bringToFront();
    await pageA.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await expect(pageA.locator('#project-remote-change')).toBeVisible({ timeout: 5000 });
    await expect(pageA.locator('#project-remote-message')).toContainText(/newer saved revision \(4\)/i);
    expect(projectID).toBeTruthy();
  } finally {
    await contextA.close();
    await contextB.close();
  }
});

test('reload keeps edits made while the latest project request is pending in both editors', async ({ page }) => {
  test.setTimeout(90000);
  const attempt = `${test.info().repeatEachIndex}-${Date.now()}`;
  const profileName = `E2E reload race profile ${attempt}`;
  const rectangleName = `E2E reload race rectangle ${attempt}`;
  const vectorName = `E2E reload race vector ${attempt}`;
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.getByLabel('Profile name').fill(profileName);
  await page.getByLabel('Work area width (mm)').fill('300');
  await page.getByLabel('Work area height (mm)').fill('200');
  await page.getByLabel('Maximum spindle value ($30)').fill('1000');
  await page.getByLabel('Minimum feed (mm/min)').fill('1');
  await page.getByLabel('Maximum feed (mm/min)').fill('6000');
  await page.getByLabel('Maximum commanded power (%)').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');

  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await page.locator('#project-name').fill(rectangleName);
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Project saved');
  const rectangleRow = page.locator('#project-list li').filter({ hasText: rectangleName });
  const rectangleID = await rectangleRow.getByRole('button', { name: `Reopen project ${rectangleName}` }).getAttribute('data-project-id');
  const vector = await page.evaluate(async (name) => {
    const headers = { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}`, 'Content-Type': 'application/json' };
    const profileID = document.querySelector('#profile-select').value;
    const result = await fetch('/api/v1/projects', { method: 'POST', headers, body: JSON.stringify({
      schema_version: 2, name, units: 'mm', profile_id: profileID, profile_revision: 1,
      objects: [{ id: 'race-shape', kind: 'path', operation_id: 'engrave', subpaths: [{
        points: [{ x_mm: 10, y_mm: 10 }, { x_mm: 30, y_mm: 10 }, { x_mm: 30, y_mm: 30 }, { x_mm: 10, y_mm: 30 }], closed: true,
      }] }],
      operations: [{ id: 'engrave', type: 'vector-engrave', feed_mm_min: 600, power_percent: 25 }],
    }) });
    if (!result.ok) throw new Error(`Could not create vector race fixture: ${result.status}`);
    return result.json();
  }, vectorName);

  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');

  async function publishNewerRevision(projectID) {
    const result = await page.evaluate(async (id) => {
      const headers = { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}`, 'Content-Type': 'application/json' };
      const current = await (await fetch(`/api/v1/projects/${id}`, { headers })).json();
      const body = {
        schema_version: current.schema_version, name: current.name, profile_id: current.profile_id,
        profile_revision: current.profile_revision, revision: current.revision,
      };
      if (current.schema_version === 1) {
        body.rectangle = { ...current.rectangle, x_mm: current.rectangle.x_mm + 2 };
        body.operation = current.operation;
      } else {
        body.units = current.units;
        body.objects = current.objects.map((object) => ({ ...object, subpaths: object.subpaths.map((path) => ({
          ...path, points: path.points.map((point) => ({ ...point, x_mm: point.x_mm + 2 })),
        })) }));
        body.operations = current.operations;
      }
      const response = await fetch(`/api/v1/projects/${id}`, { method: 'PUT', headers, body: JSON.stringify(body) });
      if (!response.ok) throw new Error(`Could not publish newer revision: ${response.status}`);
      document.dispatchEvent(new Event('visibilitychange'));
      return response.json();
    }, projectID);
    return result;
  }

  async function editDuringReload(projectID, reloadName, field, localValue, noticeSelector) {
    let unblock;
    let requestStarted;
    const gate = new Promise((resolve) => { unblock = resolve; });
    const arrived = new Promise((resolve) => { requestStarted = resolve; });
    const routePattern = `**/api/v1/projects/${projectID}`;
    await page.route(routePattern, async (route) => {
      if (route.request().method() === 'GET') {
        requestStarted();
        await gate;
      }
      await route.continue();
    });
    await page.getByRole('button', { name: reloadName }).click();
    await arrived;
    await page.locator(field).fill(localValue);
    let prompt = '';
    const dialogClosed = new Promise((resolve) => page.once('dialog', async (dialog) => {
      prompt = dialog.message();
      await dialog.dismiss();
      resolve();
    }));
    unblock();
    await dialogClosed;
    expect(prompt).toBe('Discard unsaved changes?');
    await expect(page.locator(field)).toHaveValue(localValue);
    await expect(page.locator(noticeSelector)).toBeVisible();
    await page.unroute(routePattern);
  }

  await page.getByRole('button', { name: `Reopen project ${rectangleName}` }).click();
  await publishNewerRevision(rectangleID);
  await expect(page.locator('#project-remote-change')).toBeVisible();
  await editDuringReload(rectangleID, 'Reload latest', '#rect-x', '13', '#project-remote-change');
  page.once('dialog', (dialog) => dialog.accept());
  await page.getByRole('button', { name: 'Close editor' }).click();

  await page.getByRole('button', { name: `Reopen vector project ${vectorName}` }).click();
  await publishNewerRevision(vector.id);
  await expect(page.locator('#vector-remote-change')).toBeVisible();
  await editDuringReload(vector.id, 'Reload latest', '#vector-x', '13', '#vector-remote-change');
  await expect(page.locator('#vector-x')).toHaveValue('13');
});

test('operator saves a declared profile, edits a bounded rectangle, and reviews a queued generated job', async ({ page }) => {
  test.setTimeout(90000);
  const attempt = `${test.info().repeatEachIndex}-${test.info().retry}-${Date.now()}`;
  const profileName = `E2E declared GRBL ${attempt}`;
  const projectName = `E2E panel outline ${attempt}`;
  const startRequests = [];
  const generatedReviewRequests = new Map();
  page.on('request', (request) => {
    if (request.method() === 'POST' && /\/api\/v1\/jobs\/[^/]+\/start$/.test(new URL(request.url()).pathname)) startRequests.push(request);
    const reviewPath = new URL(request.url()).pathname.match(/\/api\/v1\/jobs\/([^/]+)\/review$/);
    if (request.method() === 'GET' && reviewPath) generatedReviewRequests.set(reviewPath[1], (generatedReviewRequests.get(reviewPath[1]) || 0) + 1);
  });
  await page.goto('/designer');
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
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');

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
  await expect(page.locator('#notice .notice-message')).toContainText('Project saved');
  const projectRowBeforeReview = page.locator('#project-list li').filter({ hasText: projectName });
  await expect(projectRowBeforeReview).toContainText(profileName);
  await expect(projectRowBeforeReview).toContainText('r1');
  const reopenProject = projectRowBeforeReview.getByRole('button', { name: `Reopen project ${projectName}` });
  await expect(reopenProject).toHaveText('Reopen');
  await expect(projectRowBeforeReview.getByRole('button', { name: `Reopen ${projectName}` })).toHaveCount(0);

  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen project ${projectName}` }).click();
  await expect(page.locator('#rect-width')).toHaveValue('30');
  await page.locator('#rect-x').fill('11');
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
  await expect(page.locator('#project-save-hint')).toBeVisible();
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Project saved');

  await page.getByRole('button', { name: 'Edit profile' }).click();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
  await expect(page.locator('#profile-save-hint')).toBeVisible();
  await page.getByLabel('Work area width (mm)').fill('290');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');
  await expect(page.locator('#stale-profile-warning')).toBeVisible();
  await expect(projectRowBeforeReview).toContainText('Needs review');
  await page.locator('#profile-select').evaluate((select) => { select.value = ''; });
  const reopenDialog = new Promise((resolve) => {
    page.once('dialog', async (dialog) => {
      const message = dialog.message();
      await dialog.accept();
      resolve(message);
    });
  });
  await projectRowBeforeReview.getByRole('button', { name: `Reopen project ${projectName}` }).click();
  expect(await reopenDialog).toBe('Discard unsaved changes?');
  await expect(page.locator('#designer-status')).toContainText(/switched.*profile/i);
  await expect(page.locator('#profile-select')).toHaveValue(await projectRowBeforeReview.getAttribute('data-profile-id'));
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Project saved');
  await expect(page.locator('#stale-profile-warning')).toBeHidden();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeEnabled();
  await page.getByRole('button', { name: 'Edit profile' }).click();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeDisabled();
  await expect(page.locator('#profile-save-hint')).toBeVisible();
  await page.locator('#profile-form').getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.locator('#profile-save-hint')).toBeHidden();
  await expect(page.getByRole('button', { name: 'Generate queued job' })).toBeEnabled();

  await page.getByRole('button', { name: 'Generate queued job' }).click();
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  await page.getByRole('link', { name: 'Job library' }).click();
  const row = page.locator('#job-list li').filter({ hasText: `${projectName}.gcode` });
  await expect(row).toContainText(/queued/i);
  await expect(row.locator('.toolpath-live')).toBeVisible();
  await expect(row.locator('.toolpath-profile-bounds')).toHaveCount(1);
  await expect(row.locator('.toolpath-legend')).toContainText(/declared profile work area/i);
  await expect(row.getByRole('img', { name: /declared profile work area shown, not verified machine travel/i })).toBeVisible();
  await expect(row.getByRole('button', { name: 'Fit toolpath to view' })).toBeVisible();
  const jobID = await row.getByRole('button', { name: /^View / }).getAttribute('data-job-id');
  const reviewCount = () => generatedReviewRequests.get(jobID) || 0;
  await expect.poll(reviewCount).toBeGreaterThan(0);
  await page.waitForTimeout(2200);
  const stableReviewCount = reviewCount();
  await page.waitForTimeout(2200);
  expect(reviewCount()).toBe(stableReviewCount);
  await row.getByRole('button', { name: /^View / }).click();
  await expect(page.locator('#program-panel')).toBeVisible();
  await expect(page.locator('#program-code')).toContainText('G0 X11 Y20');
  await expect(page.locator('#program-code')).toContainText('G1 X41 Y20 F600 S250');
  await expect(page.locator('#program-provenance')).toContainText(projectName);
  await expect(page.locator('#program-provenance')).toContainText('commanded power 25%');
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  await expect(row.getByRole('button', { name: /^Start / })).toBeDisabled();
  expect(startRequests).toHaveLength(0);
  await page.locator('#operator-confirmation').check();
  await row.getByRole('button', { name: /^Start / }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Start on simulator' }).click();
  await expect.poll(() => startRequests.length).toBe(1);
  await expect(row.locator('.toolpath-live')).toHaveAttribute('aria-label', /Live toolpath for/);
  await expect(row).toContainText(/completed/i, { timeout: 30000 });

  const uploadedStaleName = await page.evaluate(async ({ jobID, apiKey, projectName }) => {
    const headers = { Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json' };
    const job = await (await fetch(`/api/v1/jobs/${jobID}`, { headers })).json();
    const profileID = job.provenance.profile.id;
    const profile = await (await fetch(`/api/v1/profiles/${profileID}`, { headers })).json();
    const uploadedName = `${projectName} uploaded.gcode`;
    const uploadResponse = await fetch('/api/v1/jobs', { method: 'POST', headers, body: JSON.stringify({ name: uploadedName, profile_id: profileID, gcode: 'G21 G90\nG0 X10 Y10\nG1 X40 Y10 F600\nM5\n' }) });
    if (!uploadResponse.ok) throw new Error(`Could not create stale uploaded test job: ${uploadResponse.status}`);
    const update = {
      schema_version: profile.schema_version, name: profile.name, controller: profile.controller,
      units: profile.units, origin: profile.origin, width_mm: 280, height_mm: profile.height_mm,
      min_s: profile.min_s, max_s: profile.max_s, min_feed_mm_min: profile.min_feed_mm_min,
      max_feed_mm_min: profile.max_feed_mm_min, max_power_percent: profile.max_power_percent,
      laser_mode_enabled: profile.laser_mode_enabled, revision: profile.revision,
    };
    const response = await fetch(`/api/v1/profiles/${profileID}`, { method: 'PUT', headers, body: JSON.stringify(update) });
    if (!response.ok) throw new Error(`Could not revise test profile: ${response.status} ${(await response.text())}`);
    return uploadedName;
  }, { jobID, apiKey, projectName });
  await page.locator('#refresh').click();
  await expect.poll(reviewCount).toBeGreaterThan(stableReviewCount);
  await expect(row.locator('.toolpath-profile-bounds')).toHaveCount(0);
  await expect(row.locator('.toolpath-legend')).toContainText(/declared profile work area is stale/i);
  const staleUploadedRow = page.locator('#job-list li').filter({ hasText: uploadedStaleName });
  await expect(staleUploadedRow.locator('.toolpath-profile-bounds')).toHaveCount(0);
  await expect(staleUploadedRow.locator('.toolpath-legend')).toContainText(/declared profile work area is stale/i);

  const generatedCode = await page.locator('#program-code').textContent();
  await page.getByRole('link', { name: 'Designer' }).click();
  const projectRow = page.locator('#project-list li').filter({ hasText: projectName });
  page.once('dialog', (dialog) => dialog.accept());
  await projectRow.getByRole('button', { name: `Delete project ${projectName}` }).click();
  await expect(projectRow).toHaveCount(0);
  await page.getByRole('link', { name: 'Job library' }).click();
  await expect(row).toBeVisible();
  await row.getByRole('button', { name: /^View / }).click();
  await expect(page.locator('#program-code')).toHaveText(generatedCode);
  await expect(page.locator('#program-provenance')).toContainText(projectName);

  await expect(row).toBeVisible();

  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('rectangle bounds accept exact three-decimal millimetre edges', async ({ page }) => {
  await page.goto('/designer');
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
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');

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
  const profileName = `Vector profile ${attempt}`;
  const startRequests = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && /\/api\/v1\/jobs\/[^/]+\/start$/.test(new URL(request.url()).pathname)) startRequests.push(request);
  });
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(profileName);
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
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');
  const vectorRow = page.locator('#vector-project-list li').filter({ hasText: projectName });
  await expect(vectorRow).toContainText(profileName);
  await expect(vectorRow).toContainText('r1');
  await expect(vectorRow.getByRole('button', { name: `Reopen vector project ${projectName}` })).toHaveText('Reopen');

  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen vector project ${projectName}` }).click();
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
  await page.getByRole('link', { name: 'Job library' }).click();
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  const row = page.locator('#job-list li').filter({ hasText: `${projectName}.gcode` });
  await expect(row).toContainText(/queued/i);
  await expect(row).toContainText(/created/i);
  await expect(row).toContainText(/bytes/i);
  await expect(row).toContainText('Generated');
  await row.getByRole('button', { name: /^View / }).click();
  await expect(page.locator('#program-code')).toContainText('M4 S0');
  await expect.poll(() => page.locator('#program-code').textContent().then((code) => (code.match(/M4 S0/g) || []).length)).toBe(3);
  await expect(page.locator('#program-code')).toContainText('F300 S500');
  await expect(page.locator('#program-provenance')).toContainText('Geometry snapshot: 3 objects · 3 paths');
  await expect(page.locator('#program-provenance')).toContainText('1. Engrave · 2 objects · feed 600 mm/min · commanded power 25%');
  await expect(page.locator('#program-provenance')).toContainText('2. Cut · 1 object · feed 300 mm/min · commanded power 50%');
  await expect(row.getByRole('button', { name: /^Start / })).toBeDisabled();
  expect(startRequests).toHaveLength(0);

  await page.locator('#operator-confirmation').check();
  await expect(page.locator('#driver-select')).toHaveValue('simulator');
  await row.getByRole('button', { name: /^Start / }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Start on simulator' }).click();
  await expect.poll(() => startRequests.length).toBe(1);
  await expect(row).toContainText(/completed/i, { timeout: 30000 });

  await page.getByRole('link', { name: 'Designer' }).click();
  await expect(page.locator('#profile-select')).toHaveValue(/.+/);
  const pathName = `E2E imported path ${attempt}`;
  const imported = await page.evaluate(async ({ name, profileName }) => {
    const profilesResponse = await fetch('/api/v1/profiles', { headers: { Authorization: `Bearer ${sessionStorage.getItem('emberdeck-api-key')}` } });
    const profile = (await profilesResponse.json()).find((candidate) => candidate.name === profileName);
    if (!profile) throw new Error(`The saved profile ${profileName} was not returned by the profile API (${profilesResponse.status})`);
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
  }, { name: pathName, profileName });
  expect(imported.objects[0].kind).toBe('path');
  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen vector project ${pathName}` }).click();
  await expect(page.getByRole('button', { name: '1. path' })).toBeVisible();
  await page.locator('#vector-width').fill('20');
  await page.locator('#vector-height').fill('20');
  let updatedPathPayload;
  page.on('request', (request) => {
    if (request.method() === 'PUT' && new URL(request.url()).pathname.endsWith(`/projects/${imported.id}`)) updatedPathPayload = request.postDataJSON();
  });
  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');
  expect(updatedPathPayload.objects[0].kind).toBe('path');
  expect(updatedPathPayload.objects[0].subpaths).toHaveLength(2);
  expect(updatedPathPayload.objects[0].subpaths[0].points).toEqual([
    { x_mm: 10, y_mm: 10 }, { x_mm: 30, y_mm: 10 }, { x_mm: 30, y_mm: 30 }, { x_mm: 10, y_mm: 30 },
  ]);
  expect(updatedPathPayload.objects[0].subpaths[1].points).toEqual([{ x_mm: 14, y_mm: 14 }, { x_mm: 26, y_mm: 26 }]);
});

test('open profile and rectangle editors fit tablet and phone viewports', async ({ page }) => {
  await page.goto('/designer');
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
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');
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

test('operator imports SVG paths and bounded text into an editable project and dispatches only after confirmation', async ({ page }) => {
  test.setTimeout(90000);
  const startRequests = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && /\/api\/v1\/jobs\/[^/]+\/start$/.test(new URL(request.url()).pathname)) startRequests.push(request);
  });
  const attempt = `${test.info().repeatEachIndex}-${test.info().retry}-${Date.now()}`;
  const projectName = `SVG import ${attempt}`;
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill(`SVG profile ${attempt}`);
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
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="300mm" height="200mm" viewBox="0 0 300 200"><g transform="translate(5 0)"><g transform="scale(1.2)"><path transform="translate(2 0)" d="M10 150 L40 150 L40 120 L10 120 Z"/></g></g><g transform="translate(0 5)"><path d="M60 190 C70 180 80 180 90 190 S110 180 120 190 Q130 180 140 190 T160 190"/></g><text x="180" y="50" font-family="emberdeck-stick-v1" font-size="7">A</text></svg>`;
  await page.locator('#vector-svg-file').setInputFiles({ name: 'panel.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(svg) });
  await expect(page.locator('#notice .notice-message')).toContainText('Imported 3 editable SVG paths');
  await expect(page.locator('#vector-object-list li')).toHaveCount(3);
  await expect(page.locator('#vector-object-list')).toContainText('Unassigned');
  await expect(page.locator('#vector-preview-content .vector-path')).toHaveCount(3);
  const previewStart = await page.locator('#vector-preview-content .vector-path').first().evaluate((path) => {
    const [, x, y] = path.getAttribute('d').match(/^M ([0-9.]+) ([0-9.]+)/);
    return { x: Number(x), y: Number(y) };
  });
  expect(previewStart.x).toBeCloseTo(63.13, 2);
  expect(previewStart.y).toBe(296);
  await expect(page.locator('#generate-vector-project')).toBeDisabled();
  const outsideSVG = `<svg width="350mm" height="200mm" viewBox="0 0 350 200"><polyline points="330,190 350,190"/></svg>`;
  await page.locator('#vector-svg-file').setInputFiles({ name: 'outside.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(outsideSVG) });
  await expect(page.locator('#vector-validation')).toContainText(/outside the declared workspace/i);
  await expect(page.locator('#vector-object-list li')).toHaveCount(4);
  await page.getByRole('button', { name: '4. path' }).click();
  await page.getByRole('button', { name: 'Remove shape 4' }).click();
  await expect(page.locator('#vector-validation')).toBeHidden();
  for (const width of [1024, 760, 390]) {
    await page.setViewportSize({ width, height: 900 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `imported vector editor fits at ${width}px`).toBe(true);
  }
  await page.setViewportSize({ width: 1440, height: 1000 });

  await page.getByRole('button', { name: '1. path' }).click();
  await page.locator('#vector-object-operation').selectOption({ label: '1. engrave' });
  await page.getByRole('button', { name: '2. path' }).click();
  await page.locator('#vector-object-operation').selectOption({ label: '1. engrave' });
  await page.getByRole('button', { name: '3. path' }).click();
  await page.locator('#vector-object-operation').selectOption({ label: '1. engrave' });
  await expect(page.locator('#vector-validation')).toBeHidden();
  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');
  await page.reload();
  await expect(page.locator('#connection-state')).toHaveText('Connected · library synced');
  await page.getByRole('button', { name: `Reopen vector project ${projectName}` }).click();
  await expect(page.locator('#vector-object-list li')).toHaveCount(3);
  await expect(page.locator('#generate-vector-project')).toBeEnabled();
  await page.locator('#generate-vector-project').click();
  await page.getByRole('link', { name: 'Job library' }).click();
  const row = page.locator('#job-list li').filter({ hasText: `${projectName}.gcode` });
  await expect(row).toContainText(/queued/i);
  await row.getByRole('button', { name: /^View / }).click();
  await expect(page.locator('#program-code')).toContainText('G1 X55.4 Y56');
  await expect(page.locator('#program-code')).toContainText('G1 X182.5 Y157');
  expect(startRequests).toHaveLength(0);
  await expect(page.locator('#operator-confirmation')).not.toBeChecked();
  await expect(page.locator('#driver-select')).toHaveValue('simulator');
  await page.locator('#operator-confirmation').check();
  await row.getByRole('button', { name: /^Start / }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Start on simulator' }).click();
  await expect.poll(() => startRequests.length).toBe(1);
  await expect(row).toContainText(/completed/i, { timeout: 30000 });
});
