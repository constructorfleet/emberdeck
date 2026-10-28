const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { test, expect } = require('@playwright/test');

const root = path.resolve(__dirname, '..');
const docs = path.join(root, 'dist', 'docs');
let staticServer;
let staticBase;

test.beforeAll(async () => {
  execFileSync(process.env.PYTHON || 'python3', ['scripts/build-docs.py'], { cwd: root, stdio: 'inherit' });
  staticServer = http.createServer((request, response) => {
    const pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
    const candidate = path.resolve(docs, `.${pathname}`);
    const inDocs = candidate === docs || candidate.startsWith(`${docs}${path.sep}`);
    const found = inDocs && fs.existsSync(candidate) && fs.statSync(candidate).isFile();
    const file = found ? candidate : path.join(docs, '404.html');
    const status = found ? 200 : 404;
    const contentType = file.endsWith('.svg') ? 'image/svg+xml' : 'text/html; charset=utf-8';
    response.writeHead(status, { 'content-type': contentType });
    fs.createReadStream(file).pipe(response);
  });
  await new Promise((resolve) => staticServer.listen(0, '127.0.0.1', resolve));
  staticBase = `http://127.0.0.1:${staticServer.address().port}`;
});

test.afterAll(async () => {
  if (staticServer) await new Promise((resolve) => staticServer.close(resolve));
});

test('rendered documentation has grouped, current, keyboard accessible navigation without broken assets or overflow', async ({ browser }) => {
  const pages = fs.readdirSync(docs).filter((name) => name.endsWith('.html'));
  expect(pages).toContain('operator-guide.html');
  expect(pages).toContain('api-routes.html');
  expect(pages).toContain('cli.html');
  expect(pages).toContain('404.html');
  const expectedGroups = {
    'Getting Started': [['Overview', 'index.html'], ['Install', 'install.html'], ['Operator console guide', 'operator-guide.html']],
    'Deploy & Run': [['Deployment', 'deployment.html'], ['Prometheus metrics', 'metrics.html'], ['Troubleshooting', 'troubleshooting.html'], ['Glossary', 'glossary.html'], ['Safety & Limitations', 'safety.html']],
    'Develop & Extend': [['Contributing', 'CONTRIBUTING.html'], ['Architecture', 'architecture.html'], ['Process plugins', 'plugins.html'], ['Browser console tests', 'browser-tests.html'], ['Shipped binary smoke test', 'artifact-tests.html'], ['Product status', 'roadmap.html'], ['Browser laser designer: architecture review', 'designer-architecture.html'], ['ESP32 worker feasibility', 'esp32-feasibility.html'], ['GRBL driver package', 'driver.html'], ['Worker control behavior', 'worker.html'], ['Serial transport', 'serial.html']],
    Reference: [['HTTP API and worker contract', 'api.html'], ['API route index', 'api-routes.html'], ['CLI reference', 'cli.html']],
  };

  for (const pageName of pages.filter((name) => name !== '404.html')) {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    await page.goto(pathToFileURL(path.join(docs, pageName)).href);
    await expect(page.getByRole('navigation', { name: 'Documentation' })).toBeVisible();
    await expect(page.locator('nav a[aria-current="page"]')).toHaveAttribute('href', pageName);
    const heading = await page.locator('main h1').innerText();
    await expect(page).toHaveTitle(`${heading} · EmberDeck docs`);
    await expect(page.locator('nav a[aria-current="page"]')).toHaveText(heading);
    await expect(page.locator('meta[name="description"]')).toHaveAttribute('content', /.+/);
    const description = await page.locator('meta[name="description"]').getAttribute('content');
    expect(description.length).toBeLessThanOrEqual(160);
    expect(description).not.toMatch(/<[^>]+>/);
    await expect(page.locator('meta[property="og:title"]')).toHaveAttribute('content', `${heading} · EmberDeck docs`);
    await expect(page.locator('meta[property="og:description"]')).toHaveAttribute('content', /.+/);
    await expect(page.locator('meta[property="og:image"]')).toHaveAttribute('content', /^https:\/\//);
    await expect(page.locator('link[rel="canonical"]')).toHaveAttribute('href', `https://constructorfleet.github.io/emberdeck/${pageName}`);
    await expect(page.locator('link[rel="icon"]')).toHaveAttribute('href', 'assets/favicon.svg');
    const currentPageName = await page.locator('nav a[aria-current="page"]').innerText();
    const currentSummary = page.locator('nav .nav-groups > details:has(a[aria-current="page"]) > summary');
    await expect(currentSummary).toHaveAccessibleName(new RegExp(`Current page: ${currentPageName}`));
    for (const [label, links] of Object.entries(expectedGroups)) {
      const group = page.locator('nav .nav-groups > details').filter({ has: page.getByText(label, { exact: true }) });
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
    for (const width of [1920, 1600, 1440, 1280, 1024, 900, 800, 768, 721, 720, 681, 390, 375, 320]) {
      await page.setViewportSize({ width, height: 900 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${pageName} fits ${width}px`).toBe(true);
      if (pageName === 'operator-guide.html') {
        const mobileMenu = page.locator('nav .mobile-menu');
        if (width <= 980) await mobileMenu.locator('summary').click();
        for (const group of await page.locator('nav .nav-groups > details').all()) {
          await group.locator('summary').click();
          const bounds = await group.locator('ul').evaluate((menu) => {
            const rect = menu.getBoundingClientRect();
            return { left: rect.left, right: rect.right, width: rect.width, viewport: innerWidth };
          });
          expect(bounds.width, `${pageName} menu is visible at ${width}px`).toBeGreaterThan(0);
          expect(bounds.left, `${pageName} menu left edge at ${width}px`).toBeGreaterThanOrEqual(0);
          expect(bounds.right, `${pageName} menu right edge at ${width}px`).toBeLessThanOrEqual(bounds.viewport);
          await group.locator('summary').click();
        }
        if (width <= 980) await mobileMenu.locator('summary').click();
      }
      if (width === 768) {
        await page.locator('nav').evaluate((nav) => { nav.style.fontSize = '20px'; });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${pageName} fits 768px with larger navigation text`).toBe(true);
        await page.locator('nav').evaluate((nav) => { nav.style.fontSize = ''; });
      }
    }
    await page.setViewportSize({ width: 1440, height: 1000 });

    await page.setViewportSize({ width: 1920, height: 1000 });
    const contentBounds = await page.locator('main').evaluate((node) => {
      const rect = node.getBoundingClientRect();
      return { left: rect.left, right: rect.right };
    });
    const navBounds = await page.locator('nav .nav-inner').evaluate((node) => {
      const rect = node.getBoundingClientRect();
      return { left: rect.left, right: rect.right };
    });
    expect(Math.abs(navBounds.left - contentBounds.left), `${pageName} nav aligns left at 1920px`).toBeLessThanOrEqual(1);
    expect(Math.abs(navBounds.right - contentBounds.right), `${pageName} nav aligns right at 1920px`).toBeLessThanOrEqual(1);

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
  const menu = keyboard.locator('nav .nav-groups > details').first();
  await expect(menu.locator('summary')).toBeFocused();
  await keyboard.keyboard.press('Enter');
  await expect(menu).toHaveAttribute('open', '');
  expect(await menu.locator('summary').evaluate((node) => getComputedStyle(node).outlineStyle)).not.toBe('none');
  const secondMenu = keyboard.locator('nav .nav-groups > details').nth(1);
  await secondMenu.locator('summary').click();
  await expect(secondMenu).toHaveAttribute('open', '');
  await expect(menu).not.toHaveAttribute('open', '');
  await keyboard.keyboard.press('Escape');
  await expect(secondMenu).not.toHaveAttribute('open', '');
  await expect(secondMenu.locator('summary')).toBeFocused();
  await secondMenu.locator('summary').click();
  await keyboard.locator('footer').click({ position: { x: 5, y: 5 } });
  await expect(secondMenu).not.toHaveAttribute('open', '');
  await secondMenu.locator('summary').focus();
  await keyboard.keyboard.press('Enter');
  await keyboard.locator('nav .brand').focus();
  await expect(secondMenu).not.toHaveAttribute('open', '');
  await keyboard.goto(pathToFileURL(path.join(docs, 'operator-guide.html')).href);
  await keyboard.getByRole('link', { name: 'Skip to content' }).focus();
  await keyboard.keyboard.press('Enter');
  await expect(keyboard.locator('#main-content')).toBeFocused();

  const mobile = await browser.newPage({ viewport: { width: 375, height: 667 }, isMobile: true, hasTouch: true });
  await mobile.goto(pathToFileURL(path.join(docs, 'operator-guide.html')).href);
  expect(await mobile.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(await mobile.locator('nav').evaluate((nav) => nav.getBoundingClientRect().height), 'closed mobile navigation stays compact').toBeLessThanOrEqual(80);
  const mobileMenu = mobile.locator('nav .mobile-menu');
  await expect(mobileMenu.locator('summary')).toHaveText('Menu');
  await mobileMenu.locator('summary').tap();
  await expect(mobileMenu).toHaveAttribute('open', '');
  await expect(mobile.locator('nav .nav-groups > details')).toHaveCount(4);
  const mobileReference = mobile.locator('nav .nav-groups > details').nth(3);
  await mobileReference.locator('summary').tap();
  await expect(mobileReference.getByRole('link', { name: 'API route index' })).toBeVisible();
  await expect(mobile.locator('nav a[aria-current="page"]')).toHaveCount(1);
  expect(await mobile.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('custom 404 has shared navigation without a current page and links home', async ({ page, request }) => {
  const favicon = await request.get(`${staticBase}/assets/favicon.svg`);
  expect(favicon.status()).toBe(200);
  expect(favicon.headers()['content-type']).toContain('image/svg+xml');

  const response = await page.goto(`${staticBase}/a-missing-page`);
  expect(response.status()).toBe(404);
  await expect(page).toHaveTitle('Page not found · EmberDeck docs');
  await expect(page.locator('meta[name="description"]')).toHaveAttribute('content', /.+/);
  await expect(page.locator('meta[property="og:title"]')).toHaveAttribute('content', 'Page not found · EmberDeck docs');
  await expect(page.locator('meta[property="og:description"]')).toHaveAttribute('content', /.+/);
  await expect(page.locator('meta[property="og:image"]')).toHaveAttribute('content', /^https:\/\//);
  await expect(page.locator('link[rel="canonical"]')).toHaveAttribute('href', 'https://constructorfleet.github.io/emberdeck/404.html');
  await expect(page.locator('link[rel="icon"]')).toHaveAttribute('href', 'assets/favicon.svg');
  await expect(page.getByRole('navigation', { name: 'Documentation' })).toBeVisible();
  await expect(page.locator('nav a[aria-current="page"]')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Page not found', level: 1 })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Home' })).toHaveAttribute('href', 'index.html');
});
