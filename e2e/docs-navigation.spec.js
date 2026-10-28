const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { test, expect } = require('@playwright/test');

const root = path.resolve(__dirname, '..');
const docs = path.join(root, 'dist', 'docs');

test.beforeAll(() => {
  execFileSync(process.env.PYTHON || 'python3', ['scripts/build-docs.py'], { cwd: root, stdio: 'inherit' });
});

test('rendered documentation has grouped, current, keyboard accessible navigation without broken assets or overflow', async ({ browser }) => {
  const pages = fs.readdirSync(docs).filter((name) => name.endsWith('.html'));
  expect(pages).toContain('operator-guide.html');
  expect(pages).toContain('api-routes.html');
  expect(pages).toContain('cli.html');
  const expectedGroups = {
    'Getting Started': [['Operator Guide', 'operator-guide.html'], ['Getting Started', 'index.html']],
    'Deploy & Run': [['Deployment', 'deployment.html'], ['Serial', 'serial.html']],
    'Develop & Extend': [['Contributing', 'CONTRIBUTING.html'], ['Architecture', 'architecture.html'], ['Plugins', 'plugins.html'], ['Browser Tests', 'browser-tests.html'], ['Artifact Tests', 'artifact-tests.html'], ['Roadmap', 'roadmap.html'], ['Designer Architecture', 'designer-architecture.html'], ['ESP32 Feasibility', 'esp32-feasibility.html']],
    Reference: [['API', 'api.html'], ['API Routes', 'api-routes.html'], ['CLI', 'cli.html'], ['Driver', 'driver.html'], ['Worker', 'worker.html']],
  };

  for (const pageName of pages) {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    await page.goto(pathToFileURL(path.join(docs, pageName)).href);
    await expect(page.getByRole('navigation', { name: 'Documentation' })).toBeVisible();
    await expect(page.locator('nav a[aria-current="page"]')).toHaveAttribute('href', pageName);
    const currentPageName = await page.locator('nav a[aria-current="page"]').innerText();
    const currentSummary = page.locator('nav details:has(a[aria-current="page"]) > summary');
    await expect(currentSummary).toHaveAccessibleName(new RegExp(`Current page: ${currentPageName}`));
    for (const [label, links] of Object.entries(expectedGroups)) {
      const group = page.locator('nav details').filter({ has: page.getByText(label, { exact: true }) });
      await expect(group.locator('summary')).toContainText(label);
      await group.locator('summary').click();
      for (const [link, href] of links) await expect(group.getByRole('link', { name: link, exact: true })).toHaveAttribute('href', href);
      await group.locator('summary').click();
    }

    const links = await page.locator('a[href]').evaluateAll((nodes) => nodes.map((node) => node.href));
    for (const href of links) {
      const url = new URL(href);
      if (url.protocol === 'file:' && url.pathname.endsWith('.html')) {
        expect(fs.existsSync(decodeURIComponent(url.pathname)), `${pageName} links to ${url.pathname}`).toBe(true);
      }
    }
    await expect.poll(() => page.locator('img').evaluateAll((nodes) => nodes.every((image) => image.complete && image.naturalWidth > 0))).toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${pageName} fits desktop`).toBe(true);
    for (const width of [1024, 768, 721, 720, 681, 390]) {
      await page.setViewportSize({ width, height: 900 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${pageName} fits ${width}px`).toBe(true);
      if (width === 768) {
        await page.locator('nav').evaluate((nav) => { nav.style.fontSize = '20px'; });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${pageName} fits 768px with larger navigation text`).toBe(true);
        await page.locator('nav').evaluate((nav) => { nav.style.fontSize = ''; });
      }
    }
    await page.setViewportSize({ width: 1440, height: 1000 });

    await page.close();
  }

  const keyboard = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  await keyboard.goto(pathToFileURL(path.join(docs, 'operator-guide.html')).href);
  await keyboard.keyboard.press('Tab');
  await expect(keyboard.getByRole('link', { name: 'Skip to content' })).toBeFocused();
  expect(await keyboard.getByRole('link', { name: 'Skip to content' }).evaluate((node) => getComputedStyle(node).outlineStyle)).not.toBe('none');
  await keyboard.keyboard.press('Tab');
  await expect(keyboard.getByRole('link', { name: 'EmberDeck' })).toBeFocused();
  await keyboard.keyboard.press('Tab');
  const menu = keyboard.locator('nav details').first();
  await expect(menu.locator('summary')).toBeFocused();
  await keyboard.keyboard.press('Enter');
  await expect(menu).toHaveAttribute('open', '');
  expect(await menu.locator('summary').evaluate((node) => getComputedStyle(node).outlineStyle)).not.toBe('none');
  await keyboard.goto(pathToFileURL(path.join(docs, 'operator-guide.html')).href);
  await keyboard.getByRole('link', { name: 'Skip to content' }).focus();
  await keyboard.keyboard.press('Enter');
  await expect(keyboard.locator('#main-content')).toBeFocused();

  const mobile = await browser.newPage({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  await mobile.goto(pathToFileURL(path.join(docs, 'operator-guide.html')).href);
  expect(await mobile.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await expect(mobile.locator('nav details')).toHaveCount(4);
  await mobile.locator('nav summary').nth(3).tap();
  await expect(mobile.locator('nav details').nth(3)).toHaveAttribute('open', '');
  await expect(mobile.locator('nav details').nth(3).getByRole('link', { name: 'API Routes' })).toBeVisible();
  expect(await mobile.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
