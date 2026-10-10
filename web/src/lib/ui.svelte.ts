// App-wide UI state: routing, preferences, theme, toasts, confirm dialogs,
// command palette registry.

import { loadPrefs, savePrefs, type Prefs } from './storage';
import { ApiError, api, type ServerConfig } from './api';
import { DEFAULT_CONFIG } from './handoff';
import { t, type Key } from './i18n/index.svelte';

// ------------------------------------------------------------ routing

export const route = $state({ path: location.pathname, hash: location.hash });

export function navigate(url: string, replace = false) {
  if (replace) history.replaceState(null, '', url);
  else history.pushState(null, '', url);
  route.path = location.pathname;
  route.hash = location.hash;
}

window.addEventListener('popstate', () => {
  route.path = location.pathname;
  route.hash = location.hash;
});
window.addEventListener('hashchange', () => {
  route.hash = location.hash;
});

// ------------------------------------------------------------ prefs/theme

export const prefs = $state<Prefs>(loadPrefs());

export function updatePrefs(p: Partial<Prefs>) {
  Object.assign(prefs, p);
  savePrefs($state.snapshot(prefs) as Prefs);
}

export function applyTheme() {
  const root = document.documentElement;
  if (prefs.theme === 'system') delete root.dataset.theme;
  else root.dataset.theme = prefs.theme;
  const dark = prefs.theme === 'dark' || (prefs.theme === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
  for (const m of document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')) {
    m.content = dark ? '#121214' : '#fafaf9';
    m.removeAttribute('media');
  }
  ui.themeVersion++;
}

export function cycleTheme() {
  const order: Prefs['theme'][] = ['system', 'light', 'dark'];
  updatePrefs({ theme: order[(order.indexOf(prefs.theme) + 1) % order.length] });
  applyTheme();
}

// ------------------------------------------------------------ toasts

export type Toast = { id: number; text: string; kind: 'info' | 'error' };
export const toasts = $state<Toast[]>([]);
let toastSeq = 0;

export function toast(text: string, kind: Toast['kind'] = 'info', ms = 2600) {
  const id = ++toastSeq;
  toasts.push({ id, text, kind });
  setTimeout(() => {
    const i = toasts.findIndex((x) => x.id === id);
    if (i >= 0) toasts.splice(i, 1);
  }, ms);
}

export function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    const key = `error.${e.code}` as Key;
    const s = t(key);
    if (s !== key) return s;
    return t('error.generic', { msg: e.message });
  }
  return t('error.generic', { msg: (e as Error)?.message ?? String(e) });
}

export function toastError(e: unknown) {
  toast(errorText(e), 'error', 4500);
}

export async function copy(text: string, label?: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast(label ?? t('copied'));
    return true;
  } catch {
    toast(t('copy.failed'), 'error');
    return false;
  }
}

// ------------------------------------------------------------ dialogs

export type ConfirmRequest = {
  title: string;
  body: string;
  confirm: string;
  danger?: boolean;
  resolve: (ok: boolean) => void;
};

export const ui = $state<{
  confirm: ConfirmRequest | null;
  palette: boolean;
  shortcuts: boolean;
  themeVersion: number;
}>({ confirm: null, palette: false, shortcuts: false, themeVersion: 0 });

export function confirmDialog(opts: Omit<ConfirmRequest, 'resolve'>): Promise<boolean> {
  return new Promise((resolve) => {
    ui.confirm = { ...opts, resolve };
  });
}

// ------------------------------------------------------------ commands

export type Command = { id: string; label: string; hint?: string; run: () => void };
const commandSources = new Map<string, () => Command[]>();

/** Register a provider of palette commands; returns an unregister function. */
export function provideCommands(key: string, f: () => Command[]): () => void {
  commandSources.set(key, f);
  return () => commandSources.delete(key);
}

export function allCommands(): Command[] {
  return [...commandSources.values()].flatMap((f) => f());
}

export const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
export const modKey = isMac ? '⌘' : 'Ctrl';

// ------------------------------------------------------------ server config

export const server = $state<{ config: ServerConfig }>({ config: DEFAULT_CONFIG });

export function loadServerConfig() {
  api
    .config()
    .then((c) => (server.config = c))
    .catch(() => {});
}
