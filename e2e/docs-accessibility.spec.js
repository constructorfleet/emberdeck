const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const axe = require('axe-core');
const { test, expect } = require('@playwright/test');

const root = path.resolve(__dirname, '..');
const docs = path.join(root, 'dist', 'docs');

test.beforeAll(() => {
  require('node:child_process').execFileSync(process.env.PYTHON || 'python3', ['scripts/build-docs.py'], { cwd: root, stdio: 'inherit' });
});

test('rendered documentation scroll regions are keyboard accessible', async ({ page }) => {
  const pages = fs.readdirSync(docs).filter((name) => name.endsWith('.html'));
  let totalRegions = 0;
  for (const pageName of pages) {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(pathToFileURL(path.join(docs, pageName)).href);

    const regions = page.locator('.scroll-region');
    totalRegions += await regions.count();
    for (const region of await regions.all()) {
      await expect(region).toHaveAttribute('tabindex', '0');
      await expect(region).toHaveAttribute('role', 'region');
      await expect(region).toHaveAttribute('aria-label', /\S+/);
    }

    await page.addScriptTag({ content: axe.source });
    const audit = await page.evaluate(async () => axe.run(document, {
      runOnly: { type: 'rule', values: ['scrollable-region-focusable'] },
    }));
    expect(audit.violations.map((violation) => violation.id), pageName).toEqual([]);

    if (pageName === 'deployment.html') {
      const index = await regions.evaluateAll((elements) => elements.findIndex((element) => element.scrollWidth > element.clientWidth));
      expect(index).toBeGreaterThanOrEqual(0);
      const scrollable = regions.nth(index);
      let reachedWithTab = false;
      for (let tab = 0; tab < 40; tab += 1) {
        await page.keyboard.press('Tab');
        if (await scrollable.evaluate((element) => element === document.activeElement)) {
          reachedWithTab = true;
          break;
        }
      }
      expect(reachedWithTab).toBe(true);
      const before = await scrollable.evaluate((element) => element.scrollLeft);
      await page.keyboard.press('ArrowRight');
      await expect.poll(() => scrollable.evaluate((element) => element.scrollLeft)).toBeGreaterThan(before);
    }
  }
  expect(totalRegions).toBeGreaterThan(0);
});

test('rendered tables scroll horizontally without breaking words on mobile', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto(pathToFileURL(path.join(docs, 'esp32-feasibility.html')).href);

  const region = page.locator('.table-scroll-region').first();
  await expect(region).toHaveAttribute('tabindex', '0');
  await expect(region).toHaveAttribute('role', 'region');
  await expect(region).toHaveAttribute('aria-label', /\S+/);
  const layout = await region.evaluate((element) => {
    const table = element.querySelector('table');
    const cell = table.querySelector('td, th');
    return {
      regionWidth: element.clientWidth,
      contentWidth: element.scrollWidth,
      overflowWrap: getComputedStyle(cell).overflowWrap,
      wordBreak: getComputedStyle(cell).wordBreak,
    };
  });
  expect(layout.contentWidth).toBeGreaterThan(layout.regionWidth);
  expect(layout.overflowWrap).toBe('normal');
  expect(layout.wordBreak).toBe('normal');

  await page.addScriptTag({ content: axe.source });
  const audit = await page.evaluate(async () => axe.run(document, {
    runOnly: { type: 'rule', values: ['scrollable-region-focusable'] },
  }));
  expect(audit.violations.map((violation) => violation.id)).toEqual([]);
});

test('long shell commands use backslash continuations', async ({ page }) => {
  await page.goto(pathToFileURL(path.join(docs, 'deployment.html')).href);

  for (const command of ['docker buildx build', 'sudo install -o root -g root -m 0644']) {
    const block = page.locator('.code-scroll-region code').filter({ hasText: command });
    await expect(block).toHaveCount(1);
    const lines = (await block.innerText()).split('\n');
    expect(lines.some((line) => line.trimEnd().endsWith('\\')), command).toBe(true);
    expect(Math.max(...lines.map((line) => line.length)), command).toBeLessThanOrEqual(72);
  }
});

test('rendered documentation exposes a separate footer landmark and mobile viewport', async ({ page }) => {
  for (const pageName of ['index.html', 'deployment.html', 'esp32-feasibility.html', '404.html']) {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(pathToFileURL(path.join(docs, pageName)).href);
    await expect(page.locator('meta[name="viewport"]')).toHaveAttribute('content', /initial-scale=1/);
    const footer = page.locator('footer');
    expect(await footer.evaluate((element) => element.closest('main'))).toBeNull();
    await expect(page.getByRole('contentinfo')).toHaveCount(1);
    const firstLink = footer.locator('a').first();
    expect((await firstLink.boundingBox()).height).toBeGreaterThanOrEqual(44);
  }
});
