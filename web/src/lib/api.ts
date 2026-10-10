// HTTP client for the API in DESIGN.md §4. Everything here deals in
// ciphertext (b64u strings / raw envelope bytes); no plaintext crosses it.

import type { Kdf } from './crypto';

export type ServerConfig = {
  version: string;
  maxBody: number;
  maxComment: number;
  maxAttachment: number;
  maxPasteAttachments: number;
  expiries: number[];
};

export type PasteHeader = {
  id: string;
  burn: boolean;
  discussion: boolean;
  salt: string;
  kdf: Kdf | null;
  createdAt: number;
  expiresAt: number | null;
};

export type Comment = {
  id: string;
  parentId: string | null;
  createdAt: number;
  updatedAt: number;
  author: 'owner' | 'guest';
  tag: string;
  deleted: boolean;
  version: number;
  content: string | null;
};

export type Attachment = {
  id: string;
  commentId: string | null;
  size: number;
  meta: string;
  data?: string;
};

export type OpenedPaste = {
  id: string;
  burn: boolean;
  discussion: boolean;
  locked: boolean;
  createdAt: number;
  updatedAt: number;
  expiresAt: number | null;
  version: number;
  content: string;
  attachments: Attachment[];
  comments: Comment[];
};

export type VersionInfo = { version: number; createdAt: number };
export type VersionContent = { version: number; createdAt: number; content: string };

export type CreateMeta = {
  burn: boolean;
  discussion: boolean;
  expiresIn: number;
  salt: string;
  kdf: Kdf | null;
  readVerifier: string;
  ownerVerifier: string;
  content: string;
  attachments: { id: string; meta: string }[];
};

export type LiveEvent =
  | { type: 'comment.created'; comment: Comment }
  | { type: 'comment.updated'; comment: Comment }
  | { type: 'comment.deleted'; id: string }
  | { type: 'attachment.created'; attachment: Attachment }
  | { type: 'paste.updated'; version: number; updatedAt: number }
  | { type: 'paste.locked'; locked: boolean }
  | { type: 'paste.deleted' };

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public body: Record<string, unknown> = {},
  ) {
    super(message);
  }
}

export type Auth = {
  read?: string;
  owner?: string | null;
  author?: string | null;
};

function headers(auth: Auth | undefined, extra: Record<string, string> = {}): Headers {
  const h = new Headers(extra);
  if (auth?.read) h.set('X-Read-Auth', auth.read);
  if (auth?.owner) h.set('X-Owner-Token', auth.owner);
  if (auth?.author) h.set('X-Author-Token', auth.author);
  return h;
}

async function request<T>(method: string, path: string, opts: { auth?: Auth; json?: unknown; body?: BodyInit } = {}): Promise<T> {
  const init: RequestInit = { method, headers: headers(opts.auth), cache: 'no-store', credentials: 'omit' };
  if (opts.json !== undefined) {
    (init.headers as Headers).set('Content-Type', 'application/json');
    init.body = JSON.stringify(opts.json);
  } else if (opts.body !== undefined) {
    init.body = opts.body;
  }
  let res: Response;
  try {
    res = await fetch('/api' + path, init);
  } catch (e) {
    throw new ApiError(0, 'network', (e as Error).message || 'network error');
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: any = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }
  if (!res.ok) {
    if (res.status === 429) {
      const ra = res.headers.get('Retry-After');
      throw new ApiError(429, 'rate_limited', data?.message ?? 'rate limited', { retryAfter: ra ? Number(ra) : null });
    }
    throw new ApiError(res.status, data?.error ?? 'http_' + res.status, data?.message ?? res.statusText, data ?? {});
  }
  return data as T;
}

const enc = (id: string) => encodeURIComponent(id);

export const api = {
  config: () => request<ServerConfig>('GET', '/config'),

  create(meta: CreateMeta, files: { id: string; data: Uint8Array<ArrayBuffer> }[]) {
    const fd = new FormData();
    fd.append('meta', new Blob([JSON.stringify(meta)], { type: 'application/json' }));
    for (const f of files) fd.append('att-' + f.id, new Blob([f.data], { type: 'application/octet-stream' }), f.id);
    return request<{ id: string; expiresAt: number | null }>('POST', '/pastes', { body: fd });
  },

  header: (id: string) => request<PasteHeader>('GET', `/pastes/${enc(id)}`),
  open: (id: string, read: string) => request<OpenedPaste>('POST', `/pastes/${enc(id)}/open`, { auth: { read } }),

  update: (id: string, owner: string, content: string, baseVersion: number) =>
    request<{ version: number; updatedAt: number }>('PUT', `/pastes/${enc(id)}`, {
      auth: { owner },
      json: { content, baseVersion },
    }),
  destroy: (id: string, owner: string) => request<void>('DELETE', `/pastes/${enc(id)}`, { auth: { owner } }),
  lock: (id: string, owner: string, locked: boolean) =>
    request<{ locked: boolean }>('POST', `/pastes/${enc(id)}/lock`, { auth: { owner }, json: { locked } }),

  versions: (id: string, read: string) => request<VersionInfo[]>('GET', `/pastes/${enc(id)}/versions`, { auth: { read } }),
  version: (id: string, read: string, v: number) =>
    request<VersionContent>('GET', `/pastes/${enc(id)}/versions/${v}`, { auth: { read } }),
  commentVersions: (id: string, read: string, cid: string) =>
    request<VersionContent[]>('GET', `/pastes/${enc(id)}/comments/${enc(cid)}/versions`, { auth: { read } }),

  upload(id: string, auth: Auth, att: { id: string; commentId: string | null; meta: string }, data: Uint8Array<ArrayBuffer>) {
    const fd = new FormData();
    fd.append('meta', new Blob([JSON.stringify(att)], { type: 'application/json' }));
    fd.append('data', new Blob([data], { type: 'application/octet-stream' }), att.id);
    return request<Attachment>('POST', `/pastes/${enc(id)}/attachments`, { auth, body: fd });
  },

  async download(id: string, read: string, aid: string): Promise<Uint8Array<ArrayBuffer>> {
    const res = await fetch(`/api/pastes/${enc(id)}/attachments/${enc(aid)}`, {
      headers: headers({ read }),
      cache: 'no-store',
      credentials: 'omit',
    });
    if (!res.ok) throw new ApiError(res.status, 'http_' + res.status, res.statusText);
    return new Uint8Array(await res.arrayBuffer());
  },

  comment: (id: string, auth: Auth, body: { id: string; parentId: string | null; content: string }) =>
    request<Comment>('POST', `/pastes/${enc(id)}/comments`, { auth, json: body }),
  editComment: (id: string, auth: Auth, cid: string, content: string, baseVersion: number) =>
    request<Comment>('PUT', `/pastes/${enc(id)}/comments/${enc(cid)}`, { auth, json: { content, baseVersion } }),
  deleteComment: (id: string, auth: Auth, cid: string) =>
    request<void>('DELETE', `/pastes/${enc(id)}/comments/${enc(cid)}`, { auth }),
};

/**
 * Subscribe to a paste's live events using fetch streaming (so the read-auth
 * header can be sent). Reconnects with backoff; `onReconnect` fires after a
 * gap so the caller can resync anything it missed.
 */
export function subscribe(
  id: string,
  read: string,
  onEvent: (e: LiveEvent) => void,
  onReconnect: () => void,
): () => void {
  let stopped = false;
  let ctrl: AbortController | null = null;
  let attempt = 0;
  let connectedOnce = false;

  const run = async () => {
    while (!stopped) {
      ctrl = new AbortController();
      try {
        const res = await fetch(`/api/pastes/${enc(id)}/events`, {
          headers: headers({ read }, { Accept: 'text/event-stream' }),
          signal: ctrl.signal,
          cache: 'no-store',
          credentials: 'omit',
        });
        if (res.status === 404) {
          onEvent({ type: 'paste.deleted' });
          return;
        }
        if (!res.ok || !res.body) throw new Error('events ' + res.status);
        if (connectedOnce) onReconnect();
        connectedOnce = true;
        attempt = 0;
        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
        let buf = '';
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buf += value;
          let idx: number;
          while ((idx = buf.indexOf('\n\n')) >= 0) {
            const block = buf.slice(0, idx);
            buf = buf.slice(idx + 2);
            const data = block
              .split('\n')
              .filter((l) => l.startsWith('data:'))
              .map((l) => l.slice(5).trimStart())
              .join('\n');
            if (!data) continue;
            try {
              const ev = JSON.parse(data) as LiveEvent;
              onEvent(ev);
              if (ev.type === 'paste.deleted') return;
            } catch {
              /* ignore malformed */
            }
          }
        }
      } catch {
        if (stopped) return;
      }
      attempt++;
      const delay = Math.min(30000, 1000 * 2 ** Math.min(attempt, 5)) * (0.5 + Math.random() / 2);
      await new Promise((r) => setTimeout(r, delay));
    }
  };
  run();
  return () => {
    stopped = true;
    ctrl?.abort();
  };
}
