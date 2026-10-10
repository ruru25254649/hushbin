// Domain layer: turns user intent (create, open, edit, comment, attach) into
// encrypted API calls, and server responses back into plaintext.

import { api, type Attachment, type Auth, type Comment, type OpenedPaste, type PasteHeader } from './api';
import { fromB64u, newId, randomBytes, toB64u, TOKEN_RE } from './b64';
import {
  DEFAULT_KDF,
  derivePasswordKey,
  deriveKeys,
  seal,
  sealJSON,
  sha256,
  unseal,
  unsealJSON,
  type AttMetaPlain,
  type BodyPlain,
  type Bytes,
  type CommentPlain,
  type PasteKeys,
} from './crypto';
import { secrets } from './storage';

// ---------------------------------------------------------------- links

export type Fragment = { key: Bytes; owner: string | null };

/** `#<K>` or `#<K>.<ownerToken>` (owner link). */
export function parseFragment(hash: string): Fragment | null {
  const h = hash.replace(/^#/, '');
  const [k, owner] = h.split('.', 2);
  if (!k || !TOKEN_RE.test(k)) return null;
  if (owner !== undefined && !TOKEN_RE.test(owner)) return null;
  try {
    const key = fromB64u(k);
    if (key.length !== 32) return null;
    return { key, owner: owner ?? null };
  } catch {
    return null;
  }
}

export function shareLink(id: string, key: Bytes): string {
  return `${location.origin}/p/${id}#${toB64u(key)}`;
}

export function ownerLink(id: string, key: Bytes, owner: string): string {
  return `${shareLink(id, key)}.${owner}`;
}

// ---------------------------------------------------------------- keys

export type Unlocked = {
  id: string;
  key: Bytes;
  p: Bytes | null;
  keys: PasteKeys;
  read: string;
};

export async function unlock(header: PasteHeader, key: Bytes, password: string | null, rememberedP?: Bytes | null): Promise<Unlocked> {
  const salt = fromB64u(header.salt);
  let p: Bytes | null = null;
  if (header.kdf) {
    if (rememberedP) p = rememberedP;
    else if (password !== null) p = await derivePasswordKey(password, salt, header.kdf);
    else throw new Error('password required');
  }
  const keys = await deriveKeys(key, p, salt);
  return { id: header.id, key, p, keys, read: toB64u(keys.readAuth) };
}

export function rememberedP(id: string): Bytes | null {
  const s = secrets.pw(id);
  if (!s) return null;
  try {
    const b = fromB64u(s);
    return b.length === 32 ? b : null;
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------- files

export type LocalFile = { id: string; name: string; type: string; data: Bytes };

export function isImage(type: string): boolean {
  return /^image\/(png|jpe?g|gif|webp|avif|bmp|svg\+xml)$/i.test(type);
}

export async function readFile(file: File): Promise<LocalFile> {
  return {
    id: newId(),
    name: file.name || (isImage(file.type) ? 'image.' + (file.type.split('/')[1] || 'png') : 'file'),
    type: file.type || 'application/octet-stream',
    data: new Uint8Array(await file.arrayBuffer()),
  };
}

/** Markdown snippet that references an attachment. */
export function attachmentMarkdown(f: { id: string; name: string; type: string }): string {
  const label = f.name.replace(/[[\]\\]/g, '');
  return isImage(f.type) ? `![${label}](att:${f.id})` : `[${label}](att:${f.id})`;
}

async function sealFile(keys: PasteKeys, f: LocalFile) {
  const meta: AttMetaPlain = { v: 1, name: f.name, type: f.type, size: f.data.length };
  const metaCt = await sealJSON(keys.encKey, 'attmeta', f.id, meta);
  const dataCt = await seal(keys.encKey, 'attdata', f.id, f.data, false);
  return { meta: toB64u(metaCt), data: dataCt };
}

// ---------------------------------------------------------------- create

export type CreateInput = {
  burn: boolean;
  discussion: boolean;
  expiresIn: number;
  password: string | null;
  rememberPassword: boolean;
  title: string;
  body: string;
  files: LocalFile[];
};

export type Created = { id: string; unlocked: Unlocked; owner: string; expiresAt: number | null };

export async function createPaste(input: CreateInput): Promise<Created> {
  const key = randomBytes(32);
  const salt = randomBytes(16);
  const kdf = input.password ? DEFAULT_KDF : null;
  const p = input.password ? await derivePasswordKey(input.password, salt, DEFAULT_KDF) : null;
  const keys = await deriveKeys(key, p, salt);
  const ownerRaw = randomBytes(32);
  const owner = toB64u(ownerRaw);

  const body: BodyPlain = { v: 1, title: input.title, body: input.body };
  const content = toB64u(await sealJSON(keys.encKey, 'body', 'body', body));
  const sealed = await Promise.all(input.files.map((f) => sealFile(keys, f)));

  const res = await api.create(
    {
      burn: input.burn,
      discussion: input.burn ? false : input.discussion,
      expiresIn: input.expiresIn,
      salt: toB64u(salt),
      kdf,
      readVerifier: toB64u(await sha256(keys.readAuth)),
      ownerVerifier: toB64u(await sha256(ownerRaw)),
      content,
      attachments: input.files.map((f, i) => ({ id: f.id, meta: sealed[i].meta })),
    },
    input.files.map((f, i) => ({ id: f.id, data: sealed[i].data })),
  );

  secrets.setOwner(res.id, owner);
  if (p && input.rememberPassword) secrets.setPw(res.id, toB64u(p));
  return {
    id: res.id,
    owner,
    expiresAt: res.expiresAt,
    unlocked: { id: res.id, key, p, keys, read: toB64u(keys.readAuth) },
  };
}

// ---------------------------------------------------------------- read

export type PlainComment = Comment & { nick: string; body: string; broken?: boolean };

export async function decryptBody(u: Unlocked, content: string): Promise<BodyPlain> {
  return unsealJSON<BodyPlain>(u.keys.encKey, 'body', 'body', fromB64u(content));
}

export async function decryptComment(u: Unlocked, c: Comment): Promise<PlainComment> {
  if (c.deleted || !c.content) return { ...c, nick: '', body: '' };
  try {
    const plain = await unsealJSON<CommentPlain>(u.keys.encKey, 'comment', c.id, fromB64u(c.content));
    return { ...c, nick: String(plain.nick ?? '').slice(0, 64), body: String(plain.body ?? '') };
  } catch {
    return { ...c, nick: '', body: '', broken: true };
  }
}

export type OpenResult = {
  paste: OpenedPaste;
  plain: BodyPlain;
  comments: PlainComment[];
};

export async function openPaste(u: Unlocked): Promise<OpenResult> {
  const paste = await api.open(u.id, u.read);
  const plain = await decryptBody(u, paste.content);
  const comments = await Promise.all(paste.comments.map((c) => decryptComment(u, c)));
  return { paste, plain, comments };
}

// ---------------------------------------------------------------- write

export async function updatePaste(u: Unlocked, owner: string, plain: BodyPlain, baseVersion: number) {
  const content = toB64u(await sealJSON(u.keys.encKey, 'body', 'body', plain));
  return api.update(u.id, owner, content, baseVersion);
}

export async function uploadFile(u: Unlocked, auth: Auth, f: LocalFile, commentId: string | null): Promise<Attachment> {
  const s = await sealFile(u.keys, f);
  return api.upload(u.id, { ...auth, read: u.read }, { id: f.id, commentId, meta: s.meta }, s.data);
}

export async function postComment(u: Unlocked, auth: Auth, id: string, parentId: string | null, nick: string, body: string) {
  const plain: CommentPlain = { v: 1, nick, body };
  const content = toB64u(await sealJSON(u.keys.encKey, 'comment', id, plain));
  return api.comment(u.id, { ...auth, read: u.read }, { id, parentId, content });
}

export async function editComment(u: Unlocked, auth: Auth, c: PlainComment, nick: string, body: string) {
  const plain: CommentPlain = { v: 1, nick, body };
  const content = toB64u(await sealJSON(u.keys.encKey, 'comment', c.id, plain));
  return api.editComment(u.id, { ...auth, read: u.read }, c.id, content, c.version);
}

/** Guest identity for this paste in this browser, created on first use. */
export function authorToken(id: string): string {
  let t = secrets.author(id);
  if (!t || !TOKEN_RE.test(t)) {
    t = toB64u(randomBytes(32));
    secrets.setAuthor(id, t);
  }
  return t;
}

/** Tag the server derives for a token: hex(SHA-256("tag:" || raw))[0:16]. */
export async function tagFor(token: string): Promise<string> {
  const raw = fromB64u(token);
  const pre = new TextEncoder().encode('tag:');
  const buf = new Uint8Array(pre.length + raw.length);
  buf.set(pre);
  buf.set(raw, pre.length);
  const h = await sha256(buf);
  return Array.from(h.subarray(0, 8), (b) => b.toString(16).padStart(2, '0')).join('');
}

// ---------------------------------------------------------------- attachments

export type ResolvedAttachment = { meta: AttMetaPlain; url: string };

/**
 * Decrypts attachments on demand and hands out object URLs. Burn pastes ship
 * their data inline (the server has already deleted it).
 */
export class AttachmentResolver {
  private raw = new Map<string, Attachment>();
  private cache = new Map<string, Promise<ResolvedAttachment>>();
  private metaCache = new Map<string, Promise<AttMetaPlain>>();

  constructor(private u: Unlocked) {}

  add(list: Attachment[]) {
    for (const a of list) this.raw.set(a.id, a);
  }

  has(id: string) {
    return this.raw.has(id);
  }

  list(): Attachment[] {
    return [...this.raw.values()];
  }

  meta(id: string): Promise<AttMetaPlain> {
    let m = this.metaCache.get(id);
    if (!m) {
      const a = this.raw.get(id);
      m = a
        ? unsealJSON<AttMetaPlain>(this.u.keys.encKey, 'attmeta', id, fromB64u(a.meta))
        : Promise.reject(new Error('unknown attachment'));
      this.metaCache.set(id, m);
    }
    return m;
  }

  resolve(id: string): Promise<ResolvedAttachment> {
    let r = this.cache.get(id);
    if (!r) {
      r = (async () => {
        const a = this.raw.get(id);
        if (!a) throw new Error('unknown attachment');
        const meta = await this.meta(id);
        const env = a.data ? fromB64u(a.data) : await api.download(this.u.id, this.u.read, id);
        const bytes = await unseal(this.u.keys.encKey, 'attdata', id, env);
        // SVG is rendered through <img>, which never executes scripts.
        const url = URL.createObjectURL(new Blob([bytes], { type: meta.type || 'application/octet-stream' }));
        return { meta, url };
      })();
      r.catch(() => this.cache.delete(id));
      this.cache.set(id, r);
    }
    return r;
  }

  /** Seed with a local file we just uploaded (no round-trip needed). */
  seedLocal(f: LocalFile) {
    const url = URL.createObjectURL(new Blob([f.data], { type: f.type }));
    const meta: AttMetaPlain = { v: 1, name: f.name, type: f.type, size: f.data.length };
    this.metaCache.set(f.id, Promise.resolve(meta));
    this.cache.set(f.id, Promise.resolve({ meta, url }));
  }

  dispose() {
    for (const p of this.cache.values()) p.then((r) => URL.revokeObjectURL(r.url)).catch(() => {});
    this.cache.clear();
  }
}
