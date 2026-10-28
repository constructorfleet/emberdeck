const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const { test, expect } = require('@playwright/test');

const apiKey = 'emberdeck-e2e-test-key';
const jpeg = fs.readFileSync(path.join(__dirname, 'fixtures/camera-demo.jpg'));

test('assigned job camera is authenticated, refreshed, and labeled stale when its source stops', async ({ page }) => {
  let authenticatedUpstreamRequest = false;
  const camera = http.createServer((request, response) => {
    authenticatedUpstreamRequest = request.headers.authorization === `Basic ${Buffer.from('camera-viewer:camera-secret').toString('base64')}`;
    response.writeHead(200, { 'content-type': 'image/jpeg' });
    response.end(jpeg);
  });
  await new Promise((resolve) => camera.listen(0, '127.0.0.1', resolve));
  const cameraURL = `http://127.0.0.1:${camera.address().port}/mjpeg`;
  const name = `camera-monitor-${Date.now()}-${test.info().retry}.gcode`;

  try {
    const pageResponse = await page.goto('/');
    expect(pageResponse.headers()['content-security-policy']).toContain("img-src 'self' data: blob:");
    const configured = await page.evaluate(async ({ apiKey, cameraURL }) => fetch('/api/v1/drivers/simulator/camera', {
      method: 'PUT',
      headers: { Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: cameraURL, username: 'camera-viewer', password: 'camera-secret' }),
    }).then((response) => response.status), { apiKey, cameraURL });
    expect(configured).toBe(204);
    const proxied = await page.evaluate(async (apiKey) => {
      const response = await fetch('/api/v1/drivers/simulator/camera', { headers: { Authorization: `Bearer ${apiKey}` } });
      const bytes = new Uint8Array(await response.arrayBuffer());
      return { status: response.status, type: response.headers.get('content-type'), size: bytes.length, start: Array.from(bytes.slice(0, 2)), end: Array.from(bytes.slice(-2)) };
    }, apiKey);
    expect(proxied).toMatchObject({ status: 200, type: 'image/jpeg', size: jpeg.length, start: [255, 216], end: [255, 217] });
    await page.waitForTimeout(1100);

    await connect(page);
    const program = `${Array.from({ length: 700 }, () => 'G1 X1 F100').join('\n')}\n`;
    await page.locator('#job-file').setInputFiles({ name: `${name}.gcode`, mimeType: 'text/plain', buffer: Buffer.from(program) });
    await page.locator('#job-name').fill(name);
    await page.getByRole('button', { name: /Add to library/ }).click();
    const row = page.locator('#job-list li').filter({ hasText: name });
    await expect(row).toContainText('queued');
    await page.locator('#operator-confirmation').check();
    const start = row.getByRole('button', { name: /^Start / });
    const boundsOverride = row.getByRole('checkbox', { name: /Override .* bounds review/ });
    if (await boundsOverride.count()) await boundsOverride.check();
    await expect(start).toBeEnabled();
    await start.click();
    const dialog = page.getByRole('dialog', { name: /^Start .*/ });
    await dialog.getByRole('button', { name: 'Start on simulator' }).click();

    const monitor = row.locator('.camera-monitor');
    await expect(monitor).toBeVisible();
    await expect(monitor.locator('.camera-status')).toContainText('Live camera', { timeout: 10000 });
    const image = monitor.getByRole('img', { name: `Live camera feed for ${name}` });
    await expect(image).toBeVisible();
    await expect.poll(() => image.evaluate((node) => node.naturalWidth)).toBe(480);
    expect(authenticatedUpstreamRequest).toBe(true);
    await page.screenshot({ path: 'docs/assets/screenshots/operator-camera-monitor.png', fullPage: true, animations: 'disabled' });

    await new Promise((resolve, reject) => camera.close((error) => error ? reject(error) : resolve()));
    await expect(monitor.locator('.camera-status')).toContainText('Camera offline or stale', { timeout: 6000 });
    await expect(monitor.locator('.camera-status')).toContainText('last frame');
    await expect(image).toBeVisible();
    await expect(row).toContainText(/running/i);
    await expect(row).toContainText(/completed/i, { timeout: 30000 });
  } finally {
    if (camera.listening) await new Promise((resolve) => camera.close(resolve));
    try {
      await page.evaluate(async ({ apiKey, name }) => {
        const headers = { Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json' };
        const list = await fetch(`/api/v1/jobs?name=${encodeURIComponent(name)}`, { headers });
        if (list.ok) {
          const { jobs } = await list.json();
          for (const job of jobs) await fetch(`/api/v1/jobs/${encodeURIComponent(job.id)}`, { method: 'DELETE', headers });
        }
        await fetch('/api/v1/drivers/simulator/camera', { method: 'PUT', headers, body: '{}' });
      }, { apiKey, name });
    } catch (_) { /* the test may fail before the server is configured */ }
  }
});

async function connect(page) {
  await page.locator('#api-key').fill(apiKey);
  await page.getByRole('button', { name: /Connect/ }).click();
  await expect(page.locator('#connection-controls')).toContainText('Connected');
  await expect(page.locator('#driver-select')).toHaveValue('simulator');
}
