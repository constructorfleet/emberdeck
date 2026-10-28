const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { test, expect } = require('@playwright/test');

const root = path.resolve(__dirname, '..');
const docs = path.join(root, 'dist', 'docs');

test.beforeAll(() => {
  require('node:child_process').execFileSync(process.env.PYTHON || 'python3', ['scripts/build-docs.py'], { cwd: root, stdio: 'inherit' });
});

test('rendered docs use shared color, spacing, and radius tokens', async ({ page }) => {
  await page.goto(pathToFileURL(path.join(docs, 'deployment.html')).href);
  await expect(page.locator('meta[name="color-scheme"]')).toHaveAttribute('content', 'dark');
  const styles = await page.locator('style').allTextContents();
  const css = styles.join('\n');
  expect(css).toMatch(/:root\s*\{[^}]*--color-canvas:/s);
  expect(css).toMatch(/:root\s*\{[^}]*--space-1:/s);
  expect(css).toMatch(/:root\s*\{[^}]*--radius-sm:/s);

  const withoutRootTokens = css.replace(/:root\s*\{[^}]*\}/s, '');
  expect(withoutRootTokens).not.toMatch(/#[\da-f]{3,8}\b|rgba?\(/i);
  expect(withoutRootTokens).not.toMatch(/(?:margin|padding|gap):[^;}]*\d*\.?\d+rem/i);
  await expect(page.locator('html')).toHaveCSS('color-scheme', 'dark');
});

test('code blocks use monospace styling, syntax highlighting, and working copy controls', async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: (text) => { window.__copiedCode = text; return Promise.resolve(); } },
    });
  });
  await page.goto(pathToFileURL(path.join(docs, 'deployment.html')).href);

  const inlineCode = page.locator('main p code').first();
  await expect(inlineCode).toHaveCSS('font-family', /monospace|monaco|consolas/i);
  expect(await inlineCode.evaluate((element) => getComputedStyle(element).backgroundColor)).not.toBe('rgba(0, 0, 0, 0)');

  const codeBlock = page.locator('.codehilite').filter({ hasText: 'docker buildx build' }).first();
  await expect(codeBlock.locator('pre')).toHaveCSS('line-height', '23.2px');
  await expect(page.locator('.codehilite span.k, .codehilite span.kn, .codehilite span.nb, .codehilite span.s1')).not.toHaveCount(0);
  const button = codeBlock.getByRole('button', { name: 'Copy code' });
  await button.click();
  await expect.poll(() => page.evaluate(() => window.__copiedCode)).toContain('docker');
  await expect(button).toHaveText('Copied');
});

test('printed docs use dark text on white and hide navigation controls', async ({ page }) => {
  await page.goto(pathToFileURL(path.join(docs, 'deployment.html')).href);
  await page.emulateMedia({ media: 'print' });
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(255, 255, 255)');
  await expect(page.locator('body')).toHaveCSS('color', 'rgb(0, 0, 0)');
  await expect(page.getByRole('navigation', { name: 'Documentation' })).toBeHidden();
  await expect(page.getByRole('link', { name: 'Skip to content' })).toBeHidden();
  await expect(page.locator('main a').first()).toHaveCSS('color', 'rgb(0, 0, 0)');
});

test('architecture docs record the shared dark console identity and print decision', async () => {
  const architecture = fs.readFileSync(path.join(root, 'docs', 'architecture.md'), 'utf8');
  expect(architecture).toContain('Documentation visual identity');
  expect(architecture).toMatch(/console[\s\S]*dark/i);
  expect(architecture).toMatch(/dark-only[\s\S]*print|print[\s\S]*dark-only/i);
  expect(architecture).toMatch(/driver adapter[\s\S]*in-process prototype/i);
});
