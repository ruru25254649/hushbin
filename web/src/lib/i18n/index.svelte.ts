import en, { type Dict } from './en';

export const LANGS = [
  { code: 'en', label: 'English' },
  { code: 'ja', label: '日本語' },
  { code: 'zh-CN', label: '简体中文' },
  { code: 'zh-TW', label: '繁體中文' },
] as const;
export type Lang = (typeof LANGS)[number]['code'];
export type Key = keyof Dict;

const loaders: Record<Exclude<Lang, 'en'>, () => Promise<{ default: Partial<Dict> }>> = {
  ja: () => import('./ja'),
  'zh-CN': () => import('./zh-CN'),
  'zh-TW': () => import('./zh-TW'),
};

export function detectLang(): Lang {
  for (const l of navigator.languages ?? [navigator.language]) {
    const s = l.toLowerCase();
    if (s.startsWith('ja')) return 'ja';
    if (s.startsWith('zh')) {
      if (/(tw|hk|mo|hant)/.test(s)) return 'zh-TW';
      return 'zh-CN';
    }
    if (s.startsWith('en')) return 'en';
  }
  return 'en';
}

export const i18n = $state<{ lang: Lang; dict: Partial<Dict> }>({ lang: 'en', dict: en });

export async function setLang(lang: Lang): Promise<void> {
  const dict = lang === 'en' ? en : (await loaders[lang]()).default;
  i18n.dict = dict;
  i18n.lang = lang;
  document.documentElement.lang = lang;
}

export function t(key: Key, vars?: Record<string, string | number>): string {
  let s = i18n.dict[key] ?? en[key] ?? key;
  if (vars) for (const [k, v] of Object.entries(vars)) s = s.replaceAll(`{${k}}`, String(v));
  return s;
}

/** "3 minutes ago" / "in 2 days", localized. */
export function relTime(ms: number, now = Date.now()): string {
  const diff = (ms - now) / 1000;
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat(i18n.lang, { numeric: 'auto' });
  if (abs < 45) return rtf.format(0, 'second');
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), 'day');
  if (abs < 86400 * 365) return rtf.format(Math.round(diff / (86400 * 30)), 'month');
  return rtf.format(Math.round(diff / (86400 * 365)), 'year');
}

export function absTime(ms: number): string {
  return new Intl.DateTimeFormat(i18n.lang, { dateStyle: 'medium', timeStyle: 'short' }).format(ms);
}
