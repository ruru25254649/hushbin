import { type Page, type Browser } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const here = path.dirname(fileURLToPath(import.meta.url));
export const IMAGE = path.resolve(here, '../public/icon-192.png');
export const SHOTS = process.env.HB_SHOTS ?? '';

export async function typeInEditor(page: Page, scope: string, text: string) {
  const content = page.locator(`${scope} .cm-content`).first();
  await content.click();
  await page.keyboard.insertText(text);
}

export async function newPage(browser: Browser, opts: Parameters<Browser['newContext']>[0] = {}) {
  const ctx = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'], ignoreHTTPSErrors: true, ...opts });
  return ctx.newPage();
}

export async function shot(page: Page, name: string) {
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, name + '.png'), fullPage: true });
}

/** Create a Keep paste without a password through the UI; returns its link. */
export async function createOpenPaste(page: Page, body: string, opts: { discussion?: boolean } = {}) {
  await page.goto('/');
  await page.getByRole('radio', { name: /Keep/ }).click();
  await page.getByRole('button', { name: 'No password' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Turn it off' }).click();
  if (opts.discussion) await page.getByLabel('Allow discussion').check();
  else await page.getByLabel('Allow discussion').uncheck();
  await typeInEditor(page, '.sheet', body);
  await page.getByRole('button', { name: 'Create' }).click();
  await page.waitForURL(/\/p\//);
  return page.url();
}

/**
 * Derive the read token for a password-less paste link, inside the page
 * (same HKDF as the app), so tests can call the API directly.
 */
export async function readAuthFor(page: Page, link: string): Promise<{ id: string; read: string }> {
  return page.evaluate(async (link) => {
    const u = new URL(link);
    const id = u.pathname.split('/').pop()!;
    const b64 = (s: string) => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4)), (c) => c.charCodeAt(0));
    const hdr = await (await fetch(`/api/pastes/${id}`)).json();
    const key = b64(u.hash.slice(1).split('.')[0]);
    const ikm = await crypto.subtle.importKey('raw', key, 'HKDF', false, ['deriveBits']);
    const bits = await crypto.subtle.deriveBits(
      { name: 'HKDF', hash: 'SHA-256', salt: b64(hdr.salt), info: new TextEncoder().encode('hushbin/v1/read') },
      ikm,
      256,
    );
    const read = btoa(String.fromCharCode(...new Uint8Array(bits))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    return { id, read };
  }, link);
}
