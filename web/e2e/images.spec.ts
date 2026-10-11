// Remote images through the server's proxy. Needs a server started with
// HB_IMAGE_PROXY_ALLOW_PRIVATE=true, because the fake image host below runs
// on 127.0.0.1 (which the proxy refuses by default).

import { test, expect } from '@playwright/test';
import http from 'node:http';
import { readFileSync } from 'node:fs';
import type { AddressInfo } from 'node:net';
import { IMAGE, newPage, typeInEditor, createOpenPaste, readAuthFor, shot, closeExtraPages } from './helpers';

test.afterEach(closeExtraPages);

let host: http.Server;
let origin = '';
const hits: string[] = [];

test.beforeAll(async () => {
  const png = readFileSync(IMAGE);
  host = http.createServer((req, res) => {
    hits.push(req.url ?? '');
    const path = new URL(req.url ?? '/', 'http://x').pathname;
    if (path === '/cat.png') {
      res.writeHead(200, { 'Content-Type': 'image/png' });
      res.end(png);
    } else if (path === '/fake.png') {
      // Claims to be a PNG but is HTML: the proxy must sniff and refuse it.
      res.writeHead(200, { 'Content-Type': 'image/png' });
      res.end('<html><script>alert(1)</script></html>');
    } else if (path === '/evil.svg') {
      res.writeHead(200, { 'Content-Type': 'image/svg+xml' });
      res.end('<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>');
    } else if (path === '/hop') {
      res.writeHead(302, { Location: '/cat.png' });
      res.end();
    } else {
      res.writeHead(404);
      res.end();
    }
  });
  await new Promise<void>((r) => host.listen(0, '127.0.0.1', r));
  origin = `http://127.0.0.1:${(host.address() as AddressInfo).port}`;
});

test.afterAll(() => host.close());

test('remote images load through the proxy, never directly', async ({ page, browser }) => {
  const direct: string[] = [];
  const proxyGets: string[] = [];
  const watch = (p: typeof page) =>
    p.on('request', (r) => {
      if (r.url().startsWith(origin)) direct.push(r.url());
      if (/\/api\/pastes\/[^/]+\/image$/.test(new URL(r.url()).pathname) && r.method() !== 'POST')
        proxyGets.push(`${r.method()} ${r.resourceType()} ${r.frame().url()}`);
    });
  watch(page);

  await page.goto('/');
  await typeInEditor(page, '.sheet', `![cat](${origin}/cat.png)\n\n![fake](${origin}/fake.png)\n\n![svg](${origin}/evil.svg)\n\n![hop](${origin}/hop)\n\ntext`);
  // Before the paste exists, the server has nothing to fetch for: placeholder.
  await expect(page.locator('.sheet .ext-fallback').first()).toContainText('after the paste is created');
  await shot(page, '10-ext-pending');

  await page.getByRole('button', { name: 'No password' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Turn it off' }).click();
  await page.getByLabel('Allow discussion').check();
  await page.getByRole('button', { name: 'Create' }).click();
  await page.waitForURL(/\/p\//);
  const link = page.url();

  const cat = page.locator('.paste img.ext-img[alt="cat"]');
  await expect(cat).toHaveJSProperty('naturalWidth', 192);
  await expect(page.locator('.paste img.ext-img[alt="hop"]')).toHaveJSProperty('naturalWidth', 192);
  await expect(page.locator('.paste .ext-fallback').filter({ hasText: 'fake' })).toContainText('Could not load');
  await expect(page.locator('.paste .ext-fallback').filter({ hasText: 'svg' })).toContainText('Could not load');
  await shot(page, '11-ext-loaded');

  // A reader in another browser sees the same thing.
  const reader = await newPage(browser);
  watch(reader);
  await reader.goto(link);
  await expect(reader.locator('.paste img.ext-img[alt="cat"]')).toHaveJSProperty('naturalWidth', 192);

  // Reader adds a remote image in a comment: live preview registers it, sending stores it.
  await typeInEditor(reader, '.new', `![again](${origin}/cat.png?c=1)\n\nnice`);
  await expect(reader.locator('.new .cm-lp-image img')).toHaveJSProperty('naturalWidth', 192);
  await reader.locator('.new').getByRole('button', { name: 'Send' }).click();
  await expect(page.locator('.msg img.ext-img[alt="again"]')).toHaveJSProperty('naturalWidth', 192);

  expect(direct, 'browsers must never contact the image host').toEqual([]);
  expect(proxyGets, 'the proxy is only called with POST').toEqual([]);

  // The proxy itself: only registered URLs, only images.
  const { id, read } = await readAuthFor(reader, link);
  const call = (url: string, headers: Record<string, string> = { 'X-Read-Auth': read }) =>
    reader.evaluate(
      async ([id, url, headers]) => {
        const r = await fetch(`/api/pastes/${id}/image`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', ...headers },
          body: JSON.stringify({ url }),
        });
        return { status: r.status, type: r.headers.get('content-type'), csp: r.headers.get('content-security-policy') };
      },
      [id, url, headers] as const,
    );
  const ok = await call(`${origin}/cat.png`);
  expect(ok.status).toBe(200);
  expect(ok.type).toBe('image/png');
  expect(ok.csp).toContain('sandbox');
  expect((await call(`${origin}/not-registered.png`)).status).toBe(404);
  expect((await call('https://example.com/anything.png')).status).toBe(404);
  expect((await call(`${origin}/fake.png`)).status).toBe(415);
  expect((await call(`${origin}/cat.png`, {})).status).toBe(403);
  expect((await call(`${origin}/cat.png`, { 'X-Read-Auth': read.replace(/^./, read[0] === 'A' ? 'B' : 'A') })).status).toBe(403);
});

test('send once: remote images still load after the paste is burned', async ({ page, browser }) => {
  await page.goto('/');
  await page.getByRole('radio', { name: /Send once/ }).click();
  await typeInEditor(page, '.sheet', `![cat](${origin}/cat.png?burn=1)\n\nsecret`);
  await page.getByRole('button', { name: 'Create' }).click();
  const link = await page.locator('.created input').inputValue();

  const reader = await newPage(browser);
  await reader.goto(link);
  await reader.getByRole('button', { name: 'Open and destroy' }).click();
  await expect(reader.locator('.paste .markdown')).toContainText('secret');
  await expect(reader.locator('.paste img.ext-img[alt="cat"]')).toHaveJSProperty('naturalWidth', 192);
});

test('owner previews a new remote image while editing', async ({ page }) => {
  const link = await createOpenPaste(page, 'plain text');
  await page.getByRole('button', { name: 'Edit' }).first().click();
  await page.locator('.edit-body .cm-content').click();
  await page.keyboard.press('Control+End');
  await page.keyboard.insertText(`\n\n![new](${origin}/cat.png?edit=1)\n\nafter`);
  await expect(page.locator('.edit-body .cm-lp-image img')).toHaveJSProperty('naturalWidth', 192);
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('.paste img.ext-img[alt="new"]')).toHaveJSProperty('naturalWidth', 192);
  expect(link).toContain('/p/');
});
