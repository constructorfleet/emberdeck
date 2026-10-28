const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';

function colorLiteralsOutsideRoot(css) {
  const rootStart = css.indexOf(':root');
  if (rootStart < 0) return ['missing :root token block'];
  const opening = css.indexOf('{', rootStart);
  let depth = 0;
  let closing = -1;
  for (let index = opening; index < css.length; index += 1) {
    if (css[index] === '{') depth += 1;
    if (css[index] === '}' && --depth === 0) {
      closing = index;
      break;
    }
  }
  if (closing < 0) return ['unclosed :root token block'];
  const outside = `${css.slice(0, rootStart)}${css.slice(closing + 1)}`;
  return [...outside.matchAll(/#[\da-f]{3,8}\b|\b(?:rgb|hsl)a?\s*\(/gi)].map(([literal]) => literal);
}

test('served console CSS keeps color literals in the documented token block', async ({ page }) => {
  await page.goto('/');
  const css = await page.evaluate(async () => (await fetch('/style.css')).text());
  expect(colorLiteralsOutsideRoot(css)).toEqual([]);
});

test('console text meets measured contrast and minimum type sizes', async ({ page }) => {
  await page.goto('/designer');
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-controls')).toContainText('Connected');

  await page.getByRole('button', { name: 'New profile' }).click();
  await page.locator('#profile-name').fill('Contrast profile');
  await page.locator('#profile-width').fill('300');
  await page.locator('#profile-height').fill('200');
  await page.locator('#profile-max-s').fill('1000');
  await page.locator('#profile-min-feed').fill('1');
  await page.locator('#profile-max-feed').fill('6000');
  await page.locator('#profile-max-power').fill('100');
  await page.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.locator('#notice .notice-message')).toContainText('Profile saved');
  await page.getByRole('button', { name: 'New rectangle project' }).click();
  await expect(page.locator('#preview-legend')).toBeVisible();
  await page.getByRole('button', { name: 'Edit profile' }).click();

  await page.evaluate(() => {
    const add = (parent, tag, className, text) => {
      const node = document.createElement(tag);
      node.className = className;
      node.textContent = text;
      parent.append(node);
    };
    // Use stable sample nodes rather than live status regions that the 2 s
    // library poll can replace between injection and measurement.
    const designer = document.querySelector('#designer-panel');
    document.querySelector('.intro').style.display = 'flex';
    document.querySelector('.workspace').style.display = 'grid';
    const eyebrow = document.createElement('p'); eyebrow.className = 'eyebrow';
    const eyebrowPart = document.createElement('span'); eyebrowPart.textContent = ' / '; eyebrow.append(eyebrowPart); designer.append(eyebrow);
    const uploadLabel = document.createElement('label'); uploadLabel.className = 'contrast-upload-label'; uploadLabel.textContent = 'G-code file'; designer.append(uploadLabel);
    const safetyCheck = document.createElement('label'); safetyCheck.className = 'safety-check'; safetyCheck.textContent = 'I will supervise the cut.'; designer.append(safetyCheck);
    const dropzone = document.createElement('div'); dropzone.className = 'dropzone';
    const caption = document.createElement('span'); caption.className = 'dropzone-caption'; caption.textContent = 'Plain text · up to 4 MiB'; dropzone.append(caption); designer.append(dropzone);
    const jobInfo = document.createElement('div'); jobInfo.className = 'job-info';
    const jobMeta = document.createElement('span'); jobMeta.textContent = 'Queued · updated today'; jobInfo.append(jobMeta); designer.append(jobInfo);
    const driverStatus = document.createElement('span'); driverStatus.className = 'driver-status'; driverStatus.textContent = 'Connected simulator'; designer.append(driverStatus);
    const freshness = document.createElement('p'); freshness.className = 'library-freshness stale'; freshness.textContent = 'Stale library data · Last updated today.'; designer.append(freshness);
    add(designer, 'p', 'notice notice-info', 'Example info notice');
    add(designer, 'p', 'notice notice-success', 'Example success notice');
    add(designer, 'p', 'notice notice-warning', 'Example warning notice');
    add(designer, 'p', 'notice notice-error', 'Example error variant');
    add(designer, 'p', 'form-validation', 'Example validation error');
    add(designer, 'p', 'form-validation warning', 'Example stale warning');
    add(designer, 'span', 'job-error', 'Example job error');
    add(designer, 'span', 'toolpath-caveat bounds-status-out_of_bounds', 'Out of bounds');
    add(designer, 'span', 'toolpath-caveat bounds-status-unknown', 'Bounds unknown');
    add(designer, 'span', 'bounds-status-in_bounds', 'In bounds');
    add(designer, 'p', 'library-freshness stale', 'Stale library data · Last updated today.');
  });

  const samples = await page.evaluate(() => {
    const selectors = [
      ['metadata', '.version'],
      ['metadata', '.step-number'],
      ['functional', '.hint'],
      ['metadata', '.dropzone > span:not(.upload-icon)'],
      ['metadata', 'footer'],
      ['metadata', '.eyebrow span'],
      ['metadata', '.job-info > span'],
      ['metadata', '.driver-status'],
      ['functional', '.library-freshness.stale'],
      ['functional', '#preview-legend'],
      ['functional', '.button-small'],
      ['functional', '.button-small.button-danger'],
      ['functional', '.contrast-upload-label'],
      ['functional', '.safety-check'],
      ['functional', '.designer-section > label'],
      ['functional', '#profile-name'],
      ['functional', '#profile-select'],
      ['functional', '.button-primary'],
      ['functional', '.notice'],
      ['functional', '.notice-info'],
      ['functional', '.notice-success'],
      ['functional', '.notice-warning'],
      ['functional', '.notice-error'],
      ['functional', '.form-validation:not(.warning)'],
      ['functional', '.form-validation.warning'],
      ['functional', '.job-error'],
      ['functional', '.bounds-status-out_of_bounds'],
      ['functional', '.bounds-status-unknown'],
      ['functional', '.bounds-status-in_bounds'],
    ];
    const parse = (value) => {
      const match = value.match(/[\d.]+/g);
      if (!match) return [0, 0, 0, 1];
      const channels = match.map(Number);
      return channels.length === 3 ? [...channels, 1] : channels;
    };
    const luminance = ([r, g, b]) => {
      const channel = (value) => {
        const normalized = value / 255;
        return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4;
      };
      return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
    };
    const ratio = (foreground, background) => {
      const [light, dark] = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
      return (light + 0.05) / (dark + 0.05);
    };
    const effectiveBackground = (target) => {
      let background = [16, 19, 19, 1];
      const ancestry = [];
      for (let node = target; node; node = node.parentElement) ancestry.push(node);
      for (const node of ancestry.reverse()) {
        const candidate = parse(getComputedStyle(node).backgroundColor);
        if (candidate[3] > 0) {
          background = candidate.slice(0, 3).map((channel, index) => channel * candidate[3] + background[index] * (1 - candidate[3])).concat(1);
        }
      }
      return background;
    };
    const visible = (element) => {
      const style = getComputedStyle(element);
      return style.display !== 'none' && style.visibility !== 'hidden' && element.getClientRects().length > 0;
    };
    return selectors.flatMap(([kind, selector]) => {
      const element = [...document.querySelectorAll(selector)].find(visible);
      if (!element) return [{ kind, selector, missing: true }];
      const style = getComputedStyle(element);
      const background = effectiveBackground(element);
      const foreground = parse(style.color).slice(0, 3);
      const ownOpacity = Number(style.opacity) || 1;
      const actualForeground = foreground.map((channel, index) => channel * ownOpacity + background[index] * (1 - ownOpacity));
      const fontSize = Number.parseFloat(style.fontSize);
      const fontWeight = Number.parseInt(style.fontWeight, 10) || 400;
      const large = fontSize >= 24 || (fontSize >= 18.67 && fontWeight >= 700);
      const component = element.matches('button, input:not([type="checkbox"]):not([type="file"]), select');
      const border = parse(style.borderTopColor);
      const parentBackground = element.parentElement ? effectiveBackground(element.parentElement) : background;
      const componentRatio = component
        ? (border[3] > 0 ? ratio(border.slice(0, 3), background) : ratio(background, parentBackground))
        : null;
      return [{ kind, selector, text: element.textContent.trim().slice(0, 50), fontSize, ratio: ratio(actualForeground, background), required: large ? 3 : 4.5, componentRatio }];
    });
  });

  console.log('Computed console contrast/type samples:', JSON.stringify(samples));
  for (const sample of samples) {
    expect.soft(sample.missing, `${sample.selector} is rendered for the contrast audit`).toBeFalsy();
    if (sample.missing) continue;
    expect.soft(sample.fontSize, `${sample.selector} font size`).toBeGreaterThanOrEqual(sample.kind === 'metadata' ? 11 : 12);
    expect.soft(sample.ratio, `${sample.selector} contrast ratio (${sample.ratio.toFixed(2)}:1; needs ${sample.required}:1)`).toBeGreaterThanOrEqual(sample.required);
    if (sample.componentRatio !== null) expect.soft(sample.componentRatio, `${sample.selector} component boundary contrast`).toBeGreaterThanOrEqual(3);
  }

  const dimensions = await page.evaluate(() => ({
    primary: document.querySelector('.designer-actions .button-primary').getBoundingClientRect().height,
    small: document.querySelector('.button-small').getBoundingClientRect().height,
    primaryFont: getComputedStyle(document.querySelector('.designer-actions .button-primary')).fontSize,
    smallFont: getComputedStyle(document.querySelector('.button-small')).fontSize,
    textInput: document.querySelector('#profile-name').getBoundingClientRect().height,
    select: document.querySelector('#profile-select').getBoundingClientRect().height,
    inputFont: getComputedStyle(document.querySelector('#profile-name')).fontSize,
    selectFont: getComputedStyle(document.querySelector('#profile-select')).fontSize,
    uploadIcon: document.querySelector('.upload-icon').getBoundingClientRect().width,
    destructiveFill: getComputedStyle(document.querySelector('.button-small.button-danger')).backgroundColor,
    nameGridGap: getComputedStyle(document.querySelector('.numeric-grid')).marginTop,
    wrappedLabelInputAligns: (() => {
      const labels = [...document.querySelectorAll('#profile-form .numeric-grid label')];
      const first = labels[0].querySelector('input').getBoundingClientRect();
      const second = labels[1].querySelector('input').getBoundingClientRect();
      return Math.abs(first.top - second.top) <= 1;
    })(),
  }));
  expect(dimensions.primary).toBeGreaterThanOrEqual(40);
  expect(dimensions.small).toBeGreaterThanOrEqual(36);
  expect(dimensions.primaryFont).toBe(dimensions.smallFont);
  expect(dimensions.textInput).toBe(dimensions.select);
  expect(dimensions.inputFont).toBe(dimensions.selectFont);
  expect(dimensions.uploadIcon).toBe(20);
  expect(dimensions.destructiveFill).not.toBe('rgba(0, 0, 0, 0)');
  expect(dimensions.nameGridGap).not.toBe('0px');
  expect(dimensions.wrappedLabelInputAligns).toBe(true);
});
