const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

async function connect(page) {
  await page.goto('/designer');
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

async function clickAndResolveDiscard(page, button, accept) {
  const dialogMessage = new Promise((resolve) => {
    page.once('dialog', async (dialog) => {
      const message = dialog.message();
      if (accept) await dialog.accept();
      else await dialog.dismiss();
      resolve(message);
    });
  });
  await button.click();
  expect(await dialogMessage).toBe('Discard unsaved changes?');
}

test('dirty profile and rectangle forms ask before discarding and show accurate save guidance', async ({ page }) => {
  await connect(page);
  await createProfile(page, `Discard guard ${Date.now()}`);

  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await expect(page.locator('#project-save-hint')).toHaveText('Save the project to enable generation');
  let cleanClosePrompt = false;
  const handleUnexpectedPrompt = (dialog) => {
    cleanClosePrompt = true;
    void dialog.dismiss();
  };
  page.on('dialog', handleUnexpectedPrompt);
  await page.getByRole('button', { name: 'Close editor' }).click();
  page.off('dialog', handleUnexpectedPrompt);
  expect(cleanClosePrompt).toBe(false);
  await expect(page.locator('#project-form')).toBeHidden();

  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await page.locator('#project-name').fill(`Guarded rectangle ${Date.now()}`);
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Project saved');
  const firstProjectName = await page.locator('#project-name').inputValue();
  await page.getByRole('button', { name: 'New rectangle project' }).click();
  const secondProjectName = `Second guarded rectangle ${Date.now()}`;
  await page.locator('#project-name').fill(secondProjectName);
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Project saved');
  await page.getByRole('button', { name: 'Edit profile' }).click();
  await page.locator('#profile-width').fill('290');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#stale-profile-warning')).toBeVisible();
  await expect(page.locator('#project-save-hint')).toBeHidden();
  await page.getByRole('button', { name: 'Save project' }).click();
  await expect(page.locator('#stale-profile-warning')).toBeHidden();
  await expect(page.locator('#project-save-hint')).toBeHidden();

  await page.getByRole('button', { name: `Reopen project ${firstProjectName}` }).click();
  await expect(page.locator('#project-save-hint')).toBeHidden();
  await page.locator('#rect-x').fill('11');
  await expect(page.locator('#project-save-hint')).toHaveText('Save changes before generating; the preview has unsaved edits.');

  await clickAndResolveDiscard(page, page.getByRole('button', { name: `Reopen project ${secondProjectName}` }), false);
  await expect(page.locator('#project-name')).toHaveValue(firstProjectName);
  await expect(page.locator('#rect-x')).toHaveValue('11');
  await clickAndResolveDiscard(page, page.getByRole('button', { name: `Reopen project ${secondProjectName}` }), true);
  await expect(page.locator('#project-name')).toHaveValue(secondProjectName);

  await page.locator('#rect-x').fill('12');
  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'New rectangle project' }), false);
  await expect(page.locator('#project-name')).toHaveValue(secondProjectName);
  await expect(page.locator('#rect-x')).toHaveValue('12');
  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'New rectangle project' }), true);
  await expect(page.locator('#project-name')).toHaveValue('Untitled rectangle');
  await expect(page.locator('#project-save-hint')).toHaveText('Save the project to enable generation');

  await page.getByRole('button', { name: `Reopen project ${firstProjectName}` }).click();
  await page.locator('#rect-x').fill('11');

  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'Close editor' }), false);
  await expect(page.locator('#project-form')).toBeVisible();
  await expect(page.locator('#rect-x')).toHaveValue('11');

  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'Close editor' }), true);
  await expect(page.locator('#project-form')).toBeHidden();

  await page.getByRole('button', { name: 'Edit profile' }).click();
  await page.locator('#profile-width').fill('280');
  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'New profile' }), false);
  await expect(page.locator('#profile-form')).toBeVisible();
  await expect(page.locator('#profile-width')).toHaveValue('280');
  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'New profile' }), true);
  await expect(page.locator('#profile-name')).toHaveValue('');
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.locator('#profile-form')).toBeHidden();

  await page.getByRole('button', { name: 'Edit profile' }).click();
  await page.locator('#profile-width').fill('270');
  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'Cancel', exact: true }), false);
  await expect(page.locator('#profile-form')).toBeVisible();
  await expect(page.locator('#profile-width')).toHaveValue('270');
  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'Cancel', exact: true }), true);
  await expect(page.locator('#profile-form')).toBeHidden();
});

test('vector projects ask before replacing a dirty design', async ({ page }) => {
  await connect(page);
  await createProfile(page, `Vector discard guard ${Date.now()}`);

  await page.getByRole('button', { name: 'New vector project' }).click();
  await expect(page.locator('#vector-save-hint')).toHaveText('Save the project to enable generation');
  let cleanClosePrompt = false;
  const handleUnexpectedPrompt = (dialog) => {
    cleanClosePrompt = true;
    void dialog.dismiss();
  };
  page.on('dialog', handleUnexpectedPrompt);
  await page.getByRole('button', { name: 'Close editor' }).last().click();
  page.off('dialog', handleUnexpectedPrompt);
  expect(cleanClosePrompt).toBe(false);
  await expect(page.locator('#vector-form')).toBeHidden();

  const projectName = `Vector discard ${Date.now()}`;
  await page.getByRole('button', { name: 'New vector project' }).click();
  await page.locator('#vector-name').fill(projectName);
  await page.getByRole('button', { name: 'Add rectangle' }).click();
  await page.getByRole('button', { name: 'Save vector project' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Vector project saved');

  await page.getByRole('button', { name: `Reopen vector project ${projectName}` }).click();
  await page.locator('#vector-name').fill(`${projectName} edited`);
  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'Close editor' }).last(), false);
  await expect(page.locator('#vector-name')).toHaveValue(`${projectName} edited`);
  await clickAndResolveDiscard(page, page.getByRole('button', { name: 'New vector project' }), true);
  await expect(page.locator('#vector-name')).toHaveValue('Untitled vector design');
});
