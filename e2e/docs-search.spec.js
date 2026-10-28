const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { expect, test } = require('@playwright/test');

const root = path.resolve(__dirname, '..');
const docs = path.join(root, 'dist', 'docs');
let server;
let baseURL;

test.beforeAll(async () => {
  execFileSync(process.env.PYTHON || 'python3', ['scripts/build-docs.py'], { cwd: root, stdio: 'inherit' });
  server = http.createServer((request, response) => {
    const pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
    const candidate = path.resolve(docs, `.${pathname}`);
    if (!candidate.startsWith(`${docs}${path.sep}`)) {
      response.writeHead(403).end();
      return;
    }
    const filename = fs.existsSync(candidate) && fs.statSync(candidate).isFile()
      ? candidate
      : path.join(docs, '404.html');
    response.writeHead(filename.endsWith('.json') ? 200 : filename.endsWith('.html') ? 200 : 404, {
      'content-type': filename.endsWith('.json') ? 'application/json' : 'text/html; charset=utf-8',
    });
    fs.createReadStream(filename).pipe(response);
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  baseURL = `http://127.0.0.1:${server.address().port}`;
});

test.afterAll(async () => {
  if (server) await new Promise((resolve) => server.close(resolve));
});

test('search returns heading links and opens the matching section', async ({ page }) => {
  await page.goto(`${baseURL}/search.html`);
  await expect(page.getByRole('search')).toBeVisible();
  await expect(page.locator('nav .nav-groups a[href="search.html"]')).toHaveText('Search the documentation');
  await expect(page.locator('#docs-search-status')).toHaveText('Enter a search term.');
  await page.getByLabel('Search terms').fill('api');
  await page.getByRole('button', { name: 'Search' }).click();

  const apiResult = page.locator('#docs-search-results a[href="api.html#http-api-and-worker-contract"]');
  await expect(apiResult).toHaveText('HTTP API and worker contract');
  await apiResult.click();
  await expect(page).toHaveURL(/\/api\.html#http-api-and-worker-contract$/);
  await expect(page.locator('#http-api-and-worker-contract')).toBeVisible();
});

test('search reports when there are no matching headings', async ({ page }) => {
  await page.goto(`${baseURL}/search.html`);
  await page.getByLabel('Search terms').fill('no-such-heading-emberdeck');
  await page.getByRole('button', { name: 'Search' }).click();
  await expect(page.locator('#docs-search-status')).toHaveText('No matching sections.');
  await expect(page.locator('#docs-search-results')).toBeEmpty();
});
