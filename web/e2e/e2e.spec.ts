import { test, expect, type Page, type Browser } from '@playwright/test';
import { newPage } from './helpers';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const here = path.dirname(fileURLToPath(import.meta.url));
const IMAGE = path.resolve(here, '../public/icon-192.png');
const SHOTS = process.env.HB_SHOTS ?? '';

const DOC = `## Release notes

Some **bold**, *italic*, ~~gone~~ and \`inline code\`. Euler: $e^{i\\pi} + 1 = 0$.

| Key | Value |
| --- | ----- |
| a   | 1     |

- [x] done
- [ ] todo

$$
\\int_0^1 x^2\\,dx = \\frac{1}{3}
$$

\`\`\`python
def hello():
    return "hi"
\`\`\`

\`\`\`mermaid
graph TD
  A --> B
\`\`\`
`;

async function typeInEditor(page: Page, scope: string, text: string) {
  const content = page.locator(`${scope} .cm-content`).first();
  await content.click();
  await page.keyboard.insertText(text);
}

const newContext = (browser: Browser) => newPage(browser);

async function shot(page: Page, name: string) {
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, name + '.png'), fullPage: true });
}

test('keep paste: password, rich markdown, attachments, discussion, edits, owner link, lock, destroy', async ({ page, browser }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: 'Create' })).toBeVisible();

  await page.getByPlaceholder('Title (optional)').fill('E2E paste');
  await typeInEditor(page, '.sheet', DOC + '\n');
  await page.locator('.sheet input[type=file]').setInputFiles(IMAGE);
  // Live preview renders the staged (not yet uploaded) image in place.
  await expect(page.locator('.sheet .cm-lp-image img')).toHaveJSProperty('naturalWidth', 192);

  await page.getByLabel('Allow discussion').check();
  await page.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText('Enter a password, or turn it off.')).toBeVisible();
  await page.getByPlaceholder('Password', { exact: true }).fill('correct horse');
  await shot(page, '01-new-paste');
  await page.getByRole('button', { name: 'Create' }).click();

  await expect(page).toHaveURL(/\/p\/[A-Za-z0-9]+#[A-Za-z0-9_-]{43}$/);
  const link = await page.locator('.share input').inputValue();
  expect(link).toBe(page.url());
  const id = new URL(link).pathname.split('/').pop()!;
  await expect(page.locator('.paste h1')).toHaveText('E2E paste');
  await expect(page.locator('.paste .markdown table')).toBeVisible();
  await expect(page.locator('.paste .markdown .katex').first()).toBeVisible();
  await expect(page.locator('.paste .mermaid-block svg')).toBeVisible({ timeout: 20_000 });
  await expect(page.locator('.paste img.att-img')).toHaveJSProperty('naturalWidth', 192);
  await shot(page, '02-owner-view');

  // --- reader: wrong password, then right one
  const reader = await newContext(browser);
  await reader.goto(link);
  await expect(reader.getByText('Password required')).toBeVisible();
  await reader.getByPlaceholder('Password').fill('wrong');
  await reader.getByRole('button', { name: 'Unlock' }).click();
  await expect(reader.getByText('Wrong password.')).toBeVisible();
  await reader.getByPlaceholder('Password').fill('correct horse');
  await reader.getByRole('button', { name: 'Unlock' }).click();
  await expect(reader.locator('.paste h1')).toHaveText('E2E paste');
  await expect(reader.locator('.paste .markdown .code-block')).toContainText('def hello');
  await expect(reader.locator('.paste .task-box')).toHaveCount(2);
  await expect(reader.locator('.paste img.att-img')).toHaveJSProperty('naturalWidth', 192);
  await expect(reader.locator('.paste .mermaid-block svg')).toBeVisible({ timeout: 20_000 });
  await expect(reader.locator('.owner-badge')).toHaveCount(0);

  // --- reader posts; owner sees it live
  await reader.getByPlaceholder('Nickname (optional)').fill('Bob');
  await typeInEditor(reader, '.new', 'Hello from **Bob**');
  await reader.locator('.new').getByRole('button', { name: 'Send' }).click();
  await expect(reader.locator('.msg').filter({ hasText: 'Hello from Bob' })).toBeVisible();
  await expect(page.locator('.msg').filter({ hasText: 'Hello from Bob' })).toBeVisible();
  await expect(page.locator('.msg .nick').first()).toHaveText('Bob');

  // --- owner replies (Author badge), reader sees it live
  await page.locator('.msg').filter({ hasText: 'Hello from Bob' }).getByRole('button', { name: 'Reply' }).click();
  await typeInEditor(page, '.replies', 'Reply from the owner');
  await page.locator('.replies').getByRole('button', { name: 'Send' }).click();
  await expect(reader.locator('.msg.reply').filter({ hasText: 'Reply from the owner' })).toBeVisible();
  await expect(reader.locator('.msg.reply .badge')).toHaveText('Author');

  // --- reader edits own message; owner sees "edited" and the history
  await reader.locator('.msg').filter({ hasText: 'Hello from Bob' }).first().getByRole('button', { name: 'Edit' }).click();
  await expect(reader.locator('.msg .composer .cm-content')).toBeFocused();
  await reader.keyboard.press('Control+End');
  await reader.keyboard.insertText(' (edited)');
  await reader.getByRole('button', { name: 'Save' }).click();
  const bobMsg = page.locator('.thread > .msg').filter({ hasText: 'Hello from Bob (edited)' });
  await expect(bobMsg).toBeVisible();
  await bobMsg.locator('.edited').click();
  await expect(page.getByRole('dialog').getByText('Version 2')).toBeVisible();
  await expect(page.getByRole('dialog').locator('.dl.add mark')).toContainText('(edited)');
  await page.getByRole('dialog').getByRole('button', { name: 'Close' }).click();

  // --- owner edits the paste; reader receives the new version
  await page.getByRole('button', { name: 'Edit' }).first().click();
  await page.locator('.edit-body .cm-content').click();
  await page.keyboard.press('Control+End');
  await page.keyboard.insertText('\n\nAdded by the owner.');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Saved as version 2')).toBeVisible();
  await expect(reader.locator('.paste .markdown')).toContainText('Added by the owner.');
  await expect(reader.locator('.paste .meta')).toContainText('version 2');

  // --- paste history with diff
  await page.getByRole('button', { name: 'More actions' }).click();
  await page.getByRole('menuitem', { name: 'History' }).click();
  await expect(page.getByRole('dialog').locator('.dl.add').last()).toContainText('Added by the owner.');
  await shot(page, '03-history');
  await page.getByRole('dialog').getByRole('button', { name: 'Close' }).click();

  // --- owner link on a fresh device
  const ownerTok = await page.evaluate((pid) => localStorage.getItem('hb:owner:' + pid), id);
  expect(ownerTok).toMatch(/^[A-Za-z0-9_-]{43}$/);
  const device2 = await newContext(browser);
  await device2.goto(`${link}.${ownerTok}`);
  await expect(device2).toHaveURL(link);
  await device2.getByPlaceholder('Password').fill('correct horse');
  await device2.getByLabel('Remember on this device').check();
  await device2.getByRole('button', { name: 'Unlock' }).click();
  await expect(device2.locator('.owner-badge')).toBeVisible();
  await device2.reload();
  await expect(device2.locator('.paste h1')).toHaveText('E2E paste'); // remembered password
  await expect(device2.getByRole('button', { name: 'Edit' }).first()).toBeVisible();

  // --- lock discussion: reader can no longer post
  await page.getByRole('button', { name: 'More actions' }).click();
  await page.getByRole('menuitem', { name: 'Lock discussion' }).click();
  await expect(reader.locator('.discussion h2 .locked')).toBeVisible();
  await expect(reader.locator('.new')).toHaveCount(0);

  // --- moderation: owner deletes Bob's root message → tombstone (it has a reply)
  await page.locator('.thread > .msg').filter({ hasText: 'Hello from Bob' }).getByRole('button', { name: 'Delete' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();
  await expect(reader.getByText('Message deleted')).toBeVisible();
  await shot(reader, '04-reader-discussion');

  // --- destroy: other viewers are told
  await page.getByRole('button', { name: 'More actions' }).click();
  await page.getByRole('menuitem', { name: 'Destroy now' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Destroy' }).click();
  await expect(page.getByText('This paste was destroyed.')).toBeVisible();
  await expect(reader.getByText('This paste was destroyed.')).toBeVisible();
  const late = await newContext(browser);
  await late.goto(link);
  await expect(late.getByText('This paste does not exist.', { exact: false })).toBeVisible();
});

test('send once: interstitial, burns on open, password failure does not burn', async ({ page, browser }) => {
  await page.goto('/');
  await page.getByRole('radio', { name: /Send once/ }).click();
  await typeInEditor(page, '.sheet', 'the secret is 42');
  await page.getByPlaceholder('Password (optional)').fill('pw');
  await page.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText('Link ready')).toBeVisible();
  const link = await page.locator('.created input').inputValue();
  await shot(page, '05-once-created');

  const reader = await newContext(browser);
  await reader.goto(link);
  await expect(reader.getByRole('heading', { name: 'Read once' })).toBeVisible();
  await reader.getByPlaceholder('Password').fill('nope');
  await reader.getByRole('button', { name: 'Open and destroy' }).click();
  await expect(reader.getByText('Wrong password.')).toBeVisible();
  await reader.getByPlaceholder('Password').fill('pw');
  await reader.getByRole('button', { name: 'Open and destroy' }).click();
  await expect(reader.locator('.paste .markdown')).toContainText('the secret is 42');
  await expect(reader.getByText('Destroyed on the server.', { exact: false })).toBeVisible();

  const second = await newContext(browser);
  await second.goto(link);
  await expect(second.getByText('This paste does not exist.', { exact: false })).toBeVisible();
});

test('keep without password after warning; language and theme', async ({ page, browser }) => {
  await page.goto('/');
  await page.getByRole('radio', { name: /Keep/ }).click();
  await page.getByRole('button', { name: 'No password' }).click();
  await expect(page.getByRole('dialog').getByText('Turn off the password?')).toBeVisible();
  await page.getByRole('dialog').getByRole('button', { name: 'Turn it off' }).click();
  await expect(page.getByText('No password: the link alone opens this paste.')).toBeVisible();
  await typeInEditor(page, '.sheet', 'open note');
  await page.keyboard.press('Control+Enter');
  await expect(page).toHaveURL(/\/p\//);
  const link = page.url();

  const reader = await newContext(browser);
  await reader.goto(link);
  await expect(reader.locator('.paste .markdown')).toContainText('open note');

  // A damaged key never reaches the server as a valid read.
  const bad = await newContext(browser);
  const hashAt = link.indexOf('#') + 1;
  await bad.goto(link.slice(0, hashAt) + (link[hashAt] === 'A' ? 'B' : 'A') + link.slice(hashAt + 1));
  await expect(bad.getByText(/does not exist|could not decrypt|incomplete/i)).toBeVisible();

  await page.goto('/');
  const lang = page.locator('.lang select');
  await lang.selectOption('ja');
  await expect(page.getByRole('button', { name: '作成' })).toBeVisible();
  await lang.selectOption('zh-TW');
  await expect(page.getByRole('button', { name: '建立' })).toBeVisible();
  await lang.selectOption('zh-CN');
  await expect(page.getByRole('button', { name: '创建' })).toBeVisible();
  await lang.selectOption('en');
  await expect(page.getByRole('button', { name: 'Create' })).toBeVisible();

  await page.emulateMedia({ colorScheme: 'dark' });
  await reader.emulateMedia({ colorScheme: 'dark' });
  await shot(reader, '06-dark-view');
  await reader.setViewportSize({ width: 390, height: 844 });
  await shot(reader, '07-mobile-dark');
});
