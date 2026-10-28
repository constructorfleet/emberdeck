const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

async function connect(page, path = '/designer') {
  await page.goto(path);
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
  await expect(page.getByRole('button', { name: 'New profile' })).toBeFocused();
}

async function escapeWithDiscardPrompt(page, accept) {
  const dialogPromise = page.waitForEvent('dialog');
  const pressPromise = page.keyboard.press('Escape');
  const dialog = await dialogPromise;
  expect(dialog.message()).toBe('Discard unsaved changes?');
  if (accept) await dialog.accept();
  else await dialog.dismiss();
  await pressPromise;
}

test('opening and closing a profile editor moves focus in and back to its trigger', async ({ page }) => {
  await connect(page);
  const trigger = page.getByRole('button', { name: 'New profile' });
  await trigger.click();
  await expect(page.locator('#profile-name')).toBeFocused();

  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.locator('#profile-form')).toBeHidden();
  await expect(trigger).toBeFocused();

  await createProfile(page, `Keyboard edit ${Date.now()}`);
  const editTrigger = page.getByRole('button', { name: 'Edit profile' });
  await editTrigger.click();
  await expect(page.locator('#profile-name')).toBeFocused();
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(editTrigger).toBeFocused();
});

test('opening and closing rectangle and vector editors moves focus in and back to their triggers', async ({ page }) => {
  await connect(page);
  await createProfile(page, `Keyboard focus ${Date.now()}`);

  const rectangleTrigger = page.getByRole('button', { name: 'New rectangle project' });
  await rectangleTrigger.click();
  await expect(page.locator('#project-name')).toBeFocused();
  await page.getByRole('button', { name: 'Close editor' }).first().click();
  await expect(page.locator('#project-form')).toBeHidden();
  await expect(rectangleTrigger).toBeFocused();

  const rectangleName = `Focused reopen ${Date.now()}`;
  await rectangleTrigger.click();
  await page.locator('#project-name').fill(rectangleName);
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Project saved');
  await page.getByRole('button', { name: 'Close editor' }).first().click();
  const reopenRectangle = page.getByRole('button', { name: `Reopen project ${rectangleName}` });
  await reopenRectangle.click();
  await expect(page.locator('#project-name')).toBeFocused();
  await page.getByRole('button', { name: 'Close editor' }).first().click();
  await expect(reopenRectangle).toBeFocused();

  const vectorTrigger = page.getByRole('button', { name: 'New vector project' });
  await vectorTrigger.click();
  await expect(page.locator('#vector-name')).toBeFocused();
  await page.getByRole('button', { name: 'Close editor' }).last().click();
  await expect(page.locator('#vector-form')).toBeHidden();
  await expect(vectorTrigger).toBeFocused();

  const vectorName = `Focused vector reopen ${Date.now()}`;
  await vectorTrigger.click();
  await page.locator('#vector-name').fill(vectorName);
  await page.getByRole('button', { name: 'Add rectangle' }).click();
  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');
  await page.getByRole('button', { name: 'Close editor' }).last().click();
  const reopenVector = page.getByRole('button', { name: `Reopen vector project ${vectorName}` });
  await reopenVector.click();
  await expect(page.locator('#vector-name')).toBeFocused();
  await page.getByRole('button', { name: 'Close editor' }).last().click();
  await expect(reopenVector).toBeFocused();
});

test('Escape closes a dirty rectangle editor only after the discard guard accepts', async ({ page }) => {
  await connect(page);
  await createProfile(page, `Escape guard ${Date.now()}`);
  const trigger = page.getByRole('button', { name: 'New rectangle project' });
  await trigger.click();
  await page.locator('#rect-x').fill('11');

  await escapeWithDiscardPrompt(page, false);
  await expect(page.locator('#project-form')).toBeVisible();
  await expect(page.locator('#rect-x')).toHaveValue('11');

  await escapeWithDiscardPrompt(page, true);
  await expect(page.locator('#project-form')).toBeHidden();
  await expect(trigger).toBeFocused();
});

test('Escape uses the discard guard for profile and vector editors', async ({ page }) => {
  await connect(page);
  await createProfile(page, `Editor Escape ${Date.now()}`);

  const profileTrigger = page.getByRole('button', { name: 'Edit profile' });
  await profileTrigger.click();
  await page.locator('#profile-width').fill('290');
  await escapeWithDiscardPrompt(page, false);
  await expect(page.locator('#profile-form')).toBeVisible();
  await expect(page.locator('#profile-width')).toHaveValue('290');
  await escapeWithDiscardPrompt(page, true);
  await expect(page.locator('#profile-form')).toBeHidden();
  await expect(profileTrigger).toBeFocused();

  const vectorTrigger = page.getByRole('button', { name: 'New vector project' });
  await vectorTrigger.click();
  await page.locator('#vector-name').fill('Unsaved keyboard vector');
  await escapeWithDiscardPrompt(page, false);
  await expect(page.locator('#vector-form')).toBeVisible();
  await expect(page.locator('#vector-name')).toHaveValue('Unsaved keyboard vector');
  await escapeWithDiscardPrompt(page, true);
  await expect(page.locator('#vector-form')).toBeHidden();
  await expect(vectorTrigger).toBeFocused();
});

test('opening a program panel focuses its heading and Escape returns focus to View', async ({ page }) => {
  await connect(page, '/');
  const jobName = `Keyboard focus program ${Date.now()}`;
  await page.locator('#job-name').fill(jobName);
  await page.locator('#job-file').setInputFiles({
    name: 'keyboard-focus.gcode',
    mimeType: 'text/plain',
    buffer: Buffer.from('G21\nG90\nM5\n'),
  });
  await page.getByRole('button', { name: /Add to library/ }).click();
  const view = page.getByRole('button', { name: `View ${jobName}` });
  await expect(view).toBeVisible();

  await view.click();
  await expect(page.locator('#program-panel')).toBeVisible();
  await expect(page.locator('#program-title')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.locator('#program-panel')).toBeHidden();
  await expect(view).toBeFocused();
});

test('keyboard focus is visible on navigation links and the G-code dropzone', async ({ page }) => {
  await page.goto('/');
  await page.keyboard.press('Tab');
  const brand = page.locator('.brand');
  await expect(brand).toBeFocused();
  await expect(brand).toHaveCSS('outline-color', 'rgb(193, 243, 107)');
  await expect(brand).toHaveCSS('outline-width', '2px');

  await connect(page, '/');
  await page.locator('#job-name').focus();
  await page.keyboard.press('Shift+Tab');
  await expect(page.locator('#job-file')).toBeFocused();
  await expect(page.locator('.dropzone')).toHaveCSS('outline-color', 'rgb(193, 243, 107)');
  await expect(page.locator('.dropzone')).toHaveCSS('outline-width', '2px');
});

test('reduced motion uses immediate editor scrolling and removes the button hover lift', async ({ page }) => {
  await page.addInitScript(() => {
    const nativeScrollIntoView = Element.prototype.scrollIntoView;
    window.scrollIntoViewBehaviors = [];
    Element.prototype.scrollIntoView = function (options) {
      window.scrollIntoViewBehaviors.push(options?.behavior || 'auto');
      return nativeScrollIntoView.call(this, options);
    };
  });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await connect(page);
  const trigger = page.getByRole('button', { name: 'New profile' });
  await trigger.click();
  const behaviors = await page.evaluate(() => window.scrollIntoViewBehaviors);
  expect(behaviors.at(-1)).toBe('auto');
  await trigger.hover();
  await expect(trigger).toHaveCSS('transform', 'none');
});
