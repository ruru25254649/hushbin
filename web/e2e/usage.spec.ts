// Real-usage flows: system clipboard, drag & drop, keyboard only, phone
// layout, offline app shell, and "share to Hushbin" from another app.

import { test, expect, devices } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { IMAGE, newPage, typeInEditor, createOpenPaste, shot } from './helpers';

const png = readFileSync(IMAGE).toString('base64');

test('paste a screenshot from the system clipboard', async ({ page }) => {
  await page.goto('/');
  await page.locator('.sheet .cm-content').click();
  // Put a real PNG on the OS clipboard, then press the paste shortcut.
  await page.evaluate(async (b64) => {
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    await navigator.clipboard.write([new ClipboardItem({ 'image/png': new Blob([bytes], { type: 'image/png' }) })]);
  }, png);
  await page.keyboard.press('Control+V');
  await expect(page.locator('.sheet .cm-lp-image img')).toHaveJSProperty('naturalWidth', 192);
  await page.keyboard.insertText('caption');

  // Plain text from the clipboard is inserted as-is.
  await page.evaluate(() => navigator.clipboard.writeText('\n- [ ] from clipboard'));
  await page.keyboard.press('Control+V');
  await expect(page.locator('.sheet .cm-content')).toContainText('from clipboard');
});

test('drag and drop a file onto the editor', async ({ page }) => {
  await page.goto('/');
  await page.locator('.sheet .cm-content').click();
  const dt = await page.evaluateHandle(async (b64) => {
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const d = new DataTransfer();
    d.items.add(new File([bytes], 'dropped.png', { type: 'image/png' }));
    d.items.add(new File(['hello'], 'notes.txt', { type: 'text/plain' }));
    return d;
  }, png);
  const target = page.locator('.sheet .cm-content');
  await target.dispatchEvent('dragenter', { dataTransfer: dt });
  await target.dispatchEvent('dragover', { dataTransfer: dt });
  await target.dispatchEvent('drop', { dataTransfer: dt });
  await expect(page.locator('.sheet .cm-lp-image img')).toHaveJSProperty('naturalWidth', 192);
  await expect(page.locator('.sheet .file-card')).toContainText('notes.txt');
});

test('keyboard only: create, copy link, palette, history, shortcuts', async ({ page, context }) => {
  await page.goto('/');
  await page.keyboard.insertText('# Keyboard\n\nfirst');
  await page.keyboard.press('Control+e');
  await expect(page.locator('.sheet .cm-editor')).toHaveClass(/cm-source/);
  await page.keyboard.press('Control+e');
  await expect(page.locator('.sheet .cm-editor')).toHaveClass(/cm-live/);

  // Ctrl+Enter without a password: refused, focus moves to the password field.
  await page.keyboard.press('Control+Enter');
  await expect(page.getByText('Enter a password, or turn it off.')).toBeVisible();
  await expect(page.getByPlaceholder('Password', { exact: true })).toBeFocused();
  await page.keyboard.type('pw-123');
  await page.keyboard.press('Enter');
  await page.waitForURL(/\/p\//);

  await page.keyboard.press('Control+Shift+C');
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(page.url());

  // Command palette → History.
  await page.keyboard.press('Control+k');
  await expect(page.getByPlaceholder('Type a command…')).toBeFocused();
  await page.keyboard.type('hist');
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog').getByText('Version 1')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toHaveCount(0);

  // "?" opens the shortcut list.
  await page.locator('body').click({ position: { x: 5, y: 5 } });
  await page.keyboard.press('?');
  await expect(page.getByRole('heading', { name: 'Keyboard shortcuts' })).toBeVisible();
  await page.keyboard.press('Escape');

  // Edit with the keyboard and save with Ctrl+Enter.
  await page.keyboard.press('Control+k');
  await page.keyboard.type('edit');
  await page.keyboard.press('Enter');
  await page.keyboard.press('Control+End');
  await page.keyboard.insertText('\nsecond');
  await page.keyboard.press('Control+Enter');
  await expect(page.getByText('Saved as version 2')).toBeVisible();
  expect(context).toBeTruthy();
});

test('phone: create and read without horizontal scrolling', async ({ browser }) => {
  const phone = await newPage(browser, { ...devices['Pixel 7'] });
  await phone.goto('/');
  const noHScroll = () => phone.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1);
  expect(await noHScroll()).toBe(true);
  await phone.locator('.sheet .cm-content').tap();
  await phone.keyboard.insertText('| a | b | c | d | e |\n|---|---|---|---|---|\n| 1 | 2 | 3 | 4 | 5 |\n\n```js\nconst aVeryLongLineThatShouldScrollInsideTheCodeBlockNotThePage = true;\n```\n\ntext');
  await phone.getByPlaceholder('Password', { exact: true }).fill('pw');
  await phone.getByRole('button', { name: 'Create' }).tap();
  await phone.waitForURL(/\/p\//);
  await expect(phone.locator('.paste .code-block')).toBeVisible();
  expect(await noHScroll()).toBe(true);
  await shot(phone, '20-phone-view');
  await phone.getByRole('button', { name: 'More actions' }).tap();
  await expect(phone.getByRole('menuitem', { name: 'History' })).toBeVisible();
  expect(await noHScroll()).toBe(true);
});

test('installed app: works offline and receives shared content', async ({ page, context, baseURL }) => {
  test.skip(!!baseURL?.startsWith('https:') && !process.env.HB_TRUSTED_CERT, 'service workers need a trusted certificate');
  await page.goto('/');
  await page.evaluate(() => navigator.serviceWorker.ready);
  await page.reload(); // now controlled by the service worker
  await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);

  // Share target: another app POSTs text + an image to /share.
  await page.evaluate(async (b64) => {
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const fd = new FormData();
    fd.append('title', 'Shared title');
    fd.append('text', 'shared from another app');
    fd.append('files', new File([bytes], 'shot.png', { type: 'image/png' }));
    await fetch('/share', { method: 'POST', body: fd });
  }, png);
  await page.goto('/?share=1');
  await expect(page.getByPlaceholder('Title (optional)')).toHaveValue('Shared title');
  await expect(page.locator('.sheet .cm-content')).toContainText('shared from another app');
  await expect(page.locator('.sheet .cm-lp-image img')).toHaveJSProperty('naturalWidth', 192);
  await expect(page).toHaveURL(/\/$/);

  // Offline: the app shell still loads.
  await context.setOffline(true);
  await page.reload();
  await expect(page.getByRole('button', { name: 'Create' })).toBeVisible();
  await context.setOffline(false);
});

test('owner link works on a second device; reader cannot edit', async ({ page, browser }) => {
  const link = await createOpenPaste(page, 'owned');
  await page.getByRole('button', { name: 'More actions' }).click();
  await page.getByRole('menuitem', { name: 'Copy owner link' }).click();
  await expect(page.getByText('For your own devices only.', { exact: false })).toBeVisible();
  const ownerLink = await page.evaluate(() => navigator.clipboard.readText());
  expect(ownerLink.startsWith(link + '.')).toBe(true);

  const device = await newPage(browser);
  await device.goto(ownerLink);
  await expect(device.locator('.owner-badge')).toBeVisible();
  await expect(device).toHaveURL(link); // owner token removed from the address bar

  const reader = await newPage(browser);
  await reader.goto(link);
  await expect(reader.locator('.paste .markdown')).toContainText('owned');
  await expect(reader.getByRole('button', { name: 'Edit' })).toHaveCount(0);
  await reader.getByRole('button', { name: 'More actions' }).click();
  await expect(reader.getByRole('menuitem', { name: 'Destroy now' })).toHaveCount(0);
});

test('copy text and download as Markdown', async ({ page }) => {
  await createOpenPaste(page, '**bold** line');
  await page.getByRole('button', { name: 'Copy text' }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('**bold** line');
  await page.getByRole('button', { name: 'More actions' }).click();
  const [dl] = await Promise.all([page.waitForEvent('download'), page.getByRole('menuitem', { name: 'Download as Markdown' }).click()]);
  const text = readFileSync(await dl.path(), 'utf8');
  expect(text).toBe('**bold** line');
});

test('typing into the editor does not get mangled by auto-pairing or list continuation', async ({ page }) => {
  await page.goto('/');
  await page.locator('.sheet .cm-content').click();
  // Real keystrokes, not insertText.
  await page.keyboard.type('- item one');
  await page.keyboard.press('Enter');
  await page.keyboard.type('item two');
  await page.keyboard.press('Enter');
  await page.keyboard.press('Enter');
  await page.keyboard.type('a [link](https://x.y) and "quotes" and (parens)');
  await page.keyboard.press('Control+e');
  const src = await page.locator('.sheet .cm-content').innerText();
  expect(src.replace(/​/g, '')).toBe('- item one\n- item two\n\na [link](https://x.y) and "quotes" and (parens)');
});
