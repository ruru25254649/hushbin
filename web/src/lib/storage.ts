// localStorage wrapper. Storage can be unavailable (private mode, blocked
// site data), so every access is guarded and the app works without it.

const PREFIX = 'hb:';

function get(key: string): string | null {
  try {
    return localStorage.getItem(PREFIX + key);
  } catch {
    return null;
  }
}

function set(key: string, value: string | null): void {
  try {
    if (value === null) localStorage.removeItem(PREFIX + key);
    else localStorage.setItem(PREFIX + key, value);
  } catch {
    /* ignore */
  }
}

function getJSON<T>(key: string, fallback: T): T {
  const raw = get(key);
  if (raw === null) return fallback;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

/** Per-paste secrets held by this browser. */
export const secrets = {
  owner: (id: string) => get(`owner:${id}`),
  setOwner: (id: string, token: string | null) => set(`owner:${id}`, token),
  author: (id: string) => get(`author:${id}`),
  setAuthor: (id: string, token: string | null) => set(`author:${id}`, token),
  /** Remembered Argon2id output (never the password itself). */
  pw: (id: string) => get(`pw:${id}`),
  setPw: (id: string, p: string | null) => set(`pw:${id}`, p),
  nick: (id: string) => get(`nick:${id}`),
  setNick: (id: string, nick: string | null) => set(`nick:${id}`, nick),
  forget(id: string) {
    for (const k of ['owner', 'author', 'pw', 'nick']) set(`${k}:${id}`, null);
  },
};

export type Prefs = {
  theme: 'system' | 'light' | 'dark';
  lang: string | null;
  mode: 'once' | 'keep';
  expiresIn: number;
  discussion: boolean;
  editor: 'live' | 'source';
  rememberPw: boolean;
  nick: string;
};

const DEFAULT_PREFS: Prefs = {
  theme: 'system',
  lang: null,
  mode: 'keep',
  expiresIn: 604800,
  discussion: false,
  editor: 'live',
  rememberPw: false,
  nick: '',
};

export function loadPrefs(): Prefs {
  return { ...DEFAULT_PREFS, ...getJSON<Partial<Prefs>>('prefs', {}) };
}

export function savePrefs(p: Prefs): void {
  set('prefs', JSON.stringify(p));
}

/**
 * A draft of the new-paste editor, so a reload doesn't lose text. Kept in
 * sessionStorage: plaintext must not outlive the tab.
 */
export const draft = {
  load(): string | null {
    try {
      return sessionStorage.getItem(PREFIX + 'draft');
    } catch {
      return null;
    }
  },
  save(text: string | null): void {
    try {
      if (text === null || text === '') sessionStorage.removeItem(PREFIX + 'draft');
      else sessionStorage.setItem(PREFIX + 'draft', text);
    } catch {
      /* ignore */
    }
  },
};
