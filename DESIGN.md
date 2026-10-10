# Hushbin — design

A self-hosted, end-to-end encrypted clipboard. The server stores only
ciphertext, sizes and timestamps. Everything a person reads or writes is
encrypted and decrypted in the browser.

This file is the contract between `server/` (Go) and `web/` (Svelte).

## 1. Product rules

- **Two paste types.**
  - *Send once* — burns on first open. No discussion, no editing. Password optional.
  - *Keep* — has an expiry (or never). Password on by default; the creator may
    switch it off after a warning. Discussion optional.
- **Expiry presets:** 5 min, 1 h, 1 day, 1 week, 1 month, never. Owner can
  destroy at any time.
- **Ownership without accounts.** Creating a paste generates a random owner
  token kept in the browser. The *owner link* (`#<key>.<ownerToken>`) carries
  it to the owner's other devices. Never share it.
- **Discussion** (Keep pastes only): anonymous with optional nickname, owner's
  messages carry an *Author* badge, one level of replies, Markdown + images +
  files, live updates. Messages are editable with full history; deletion is
  allowed. Owner can delete any message and lock the discussion.
- **Content:** Markdown (GFM, KaTeX math, Mermaid, highlighted code), images
  inline, other files as download cards (≤ 25 MiB each by default).
- **Editing:** live preview by default, source mode always available.
- **UI:** minimal, light/dark, mobile-first, installable (PWA + share
  target), keyboard shortcuts, languages en / ja / zh-CN / zh-TW.

## 2. Encoding conventions

- `b64u` = base64url **without padding** (RFC 4648 §5).
- All times in API payloads are Unix **milliseconds** (integers).
- Paste IDs are server-generated: 10 chars from `[A-Za-z0-9]`.
- Comment and attachment IDs are **client-generated**: 16 random bytes,
  `b64u` (22 chars, regex `^[A-Za-z0-9_-]{22}$`). The server rejects
  duplicates within a paste with `409 conflict`.
- Tokens (read auth, owner token, author token) are 32 random bytes, `b64u`
  (43 chars). Verifiers are `SHA-256(rawTokenBytes)`, `b64u`.

## 3. Cryptography (client only)

```
K        = 32 random bytes                     (URL fragment, never sent)
salt     = 16 random bytes                     (stored on server, public)
P        = password ? Argon2id(utf8(password), salt, m=65536 KiB, t=3, p=1, len=32)
                    : empty
IKM      = K || P
encKey   = HKDF-SHA256(IKM, salt, info="hushbin/v1/enc",  32)   → AES-256-GCM
readAuth = HKDF-SHA256(IKM, salt, info="hushbin/v1/read", 32)
```

- `readAuth` gates every read: the server stores `SHA-256(readAuth)` and only
  returns ciphertext to callers presenting `readAuth`. Knowing only the paste
  ID — or the URL without the password — yields nothing, and password guesses
  must go online through the rate limiter. A wrong password never burns a
  *Send once* paste.
- **Envelope** (binary): `0x01 | flags(1) | nonce(12) | AES-GCM(ct || tag)`.
  `flags` bit 0 = plaintext is `deflate-raw` compressed. AAD =
  `utf8("hb1|" + kind + "|" + objectId)`, where `kind ∈ {body, comment,
  attmeta, attdata}` and `objectId` is `"body"` for the paste body, otherwise
  the comment / attachment ID.
- Plaintexts:
  - body: `{"v":1,"title":string,"body":markdown}` (compressed)
  - comment: `{"v":1,"nick":string,"body":markdown}` (compressed)
  - attmeta: `{"v":1,"name":string,"type":mime,"size":bytes}`
  - attdata: raw file bytes (not compressed)
- The browser may remember `P` (not the password) per paste in
  `localStorage` when the reader ticks "Remember on this device".

## 4. HTTP API

Base path `/api`. Request/response bodies are JSON unless noted. Errors:

```json
{ "error": "<code>", "message": "human readable" }
```

| HTTP | code | meaning |
|---|---|---|
| 400 | `invalid` | malformed request |
| 403 | `unauthorized` | missing/wrong read auth (wrong password) |
| 403 | `forbidden` | missing/wrong owner or author token |
| 403 | `locked` | discussion locked / disabled |
| 404 | `not_found` | paste or object does not exist (or expired) |
| 409 | `conflict` | stale `baseVersion` or duplicate client ID |
| 413 | `too_large` | size / count limit exceeded |
| 429 | `rate_limited` | per-IP limit hit (`Retry-After` header set) |

Auth headers (values are `b64u` raw tokens):

- `X-Read-Auth` — required on every endpoint below except `GET /config`,
  `POST /pastes`, `GET /pastes/{id}`, and owner-only endpoints.
- `X-Owner-Token` — owner actions; also marks a comment as the owner's.
- `X-Author-Token` — guest identity for creating/editing/deleting own comments.

### Shapes

```ts
type Comment = {
  id: string; parentId: string | null;
  createdAt: number; updatedAt: number;
  author: "owner" | "guest";
  tag: string;         // 16 hex chars = hex(SHA-256("tag:" || rawToken))[0:16]; identicon seed
  deleted: boolean;    // tombstone: kept only while it still has replies
  version: number;     // 1 = never edited
  content: string | null;  // b64u envelope of current version; null when deleted
};

type Attachment = {
  id: string; commentId: string | null;
  size: number;        // ciphertext size of the data blob
  meta: string;        // b64u envelope (kind attmeta)
  data?: string;       // b64u envelope (kind attdata) — ONLY in open responses of burn pastes
};
```

### `GET /api/config`

```json
{ "version": "0.1.0",
  "maxBody": 2097152, "maxComment": 262144,
  "maxAttachment": 26214400, "maxPasteAttachments": 104857600,
  "expiries": [300, 3600, 86400, 604800, 2592000, 0] }
```

`maxBody`/`maxComment`/`maxAttachment` limit **decoded ciphertext bytes**.
`expiries` lists allowed `expiresIn` values in seconds; `0` = never (omitted
when the operator disallows it).

### `POST /api/pastes` — create (`multipart/form-data`)

Part `meta` (JSON):

```json
{ "burn": false, "discussion": true, "expiresIn": 604800,
  "salt": "<b64u 16B>", "kdf": {"alg":"argon2id","m":65536,"t":3,"p":1} | null,
  "readVerifier": "<b64u>", "ownerVerifier": "<b64u>",
  "content": "<b64u envelope>",
  "attachments": [ { "id": "<22>", "meta": "<b64u>" } ] }
```

One binary part per attachment, field name `att-<id>`, body = raw envelope
bytes. Rules: `burn` ⇒ `discussion` must be false. `expiresIn` must be in
`expiries`. → `201 {"id": "...", "expiresAt": ms|null}`.

### `GET /api/pastes/{id}` — public header (no auth)

```json
{ "id": "...", "burn": false, "discussion": true,
  "salt": "...", "kdf": {...} | null, "createdAt": ms, "expiresAt": ms | null }
```

Used to decide whether to ask for a password and whether to show the
"this will be destroyed" interstitial. Never burns.

### `POST /api/pastes/{id}/open` — read (X-Read-Auth)

```json
{ "id": "...", "burn": false, "discussion": true, "locked": false,
  "createdAt": ms, "updatedAt": ms, "expiresAt": ms | null,
  "version": 3, "content": "<b64u>",
  "attachments": [Attachment], "comments": [Comment] }
```

For burn pastes the paste and all its attachments are deleted in the same
transaction, attachment `data` is inlined, and `comments` is `[]`.

### Paste owner endpoints (X-Owner-Token)

- `PUT /api/pastes/{id}` `{"content": "<b64u>", "baseVersion": 3}` →
  `200 {"version": 4, "updatedAt": ms}`; `409 {"error":"conflict","version":N}`
  if `baseVersion` is stale. Not allowed for burn pastes (`400 invalid`).
- `DELETE /api/pastes/{id}` → `204`. Destroys everything immediately.
- `POST /api/pastes/{id}/lock` `{"locked": true}` → `200 {"locked": true}`.

### Versions (X-Read-Auth)

- `GET /api/pastes/{id}/versions` → `[{"version":1,"createdAt":ms}, ...]` ascending.
- `GET /api/pastes/{id}/versions/{v}` → `{"version":v,"createdAt":ms,"content":"<b64u>"}`.
- `GET /api/pastes/{id}/comments/{cid}/versions` →
  `[{"version":1,"createdAt":ms,"content":"<b64u>"}, ...]` ascending.

Each object keeps at most `HB_MAX_VERSIONS` (default 200) versions; further
edits → `413 too_large`.

### Attachments (X-Read-Auth)

- `POST /api/pastes/{id}/attachments` (`multipart/form-data`): part `meta` JSON
  `{"id":"<22>","commentId":null|"<22>","meta":"<b64u>"}`, part `data` = raw
  envelope. If `commentId` is null the caller must also send `X-Owner-Token`
  (paste attachment). Otherwise discussion must be enabled and, unless owner,
  not locked. → `201 Attachment` (without `data`).
- `GET /api/pastes/{id}/attachments/{aid}` → `application/octet-stream` raw
  envelope bytes.

### Comments (X-Read-Auth)

- `POST /api/pastes/{id}/comments` `{"id":"<22>","parentId":null|"<22>","content":"<b64u>"}`
  plus `X-Owner-Token` (→ author `owner`) **or** `X-Author-Token`
  (→ author `guest`). The parent must be a top-level, existing comment; a
  reply to a reply is rejected (`400`). Discussion must be enabled and not
  locked (owner may still post when locked). → `201 Comment`.
- `PUT /api/pastes/{id}/comments/{cid}` `{"content":"<b64u>","baseVersion":n}`
  — author only (owner token for owner comments, author token for guest
  comments). Not allowed on tombstones or while locked (unless owner). →
  `200 Comment`.
- `DELETE /api/pastes/{id}/comments/{cid}` — author, or paste owner
  (moderation). A top-level comment with remaining replies becomes a
  tombstone (`deleted: true`, versions + attachments removed); otherwise it is
  removed. Removing the last reply of a tombstone also removes the tombstone.
  → `204`.

### Live events — `GET /api/pastes/{id}/events` (X-Read-Auth)

Server-Sent Events (`text/event-stream`, `X-Accel-Buffering: no`), consumed
with `fetch` streaming (so headers can be sent). Each message is one
`data:` line of JSON:

```
{"type":"comment.created","comment":Comment}
{"type":"comment.updated","comment":Comment}      // edits and tombstoning
{"type":"comment.deleted","id":"<22>"}
{"type":"attachment.created","attachment":Attachment}
{"type":"paste.updated","version":4,"updatedAt":ms}
{"type":"paste.locked","locked":true}
{"type":"paste.deleted"}                           // destroyed or expired
```

A `: ping` comment is sent every 20 s. The stream ends after `paste.deleted`.

## 5. Server behaviour

- Go, single static binary, SQLite (WAL) at `$HB_DATA_DIR/hushbin.db`,
  attachment blobs at `$HB_DATA_DIR/att/<pasteId>/<attId>`.
- Expired pastes are treated as not found and swept every minute.
- Per-IP token-bucket rate limits (create, comment, upload, failed auth,
  general). Client IP comes from `X-Forwarded-For` only when the peer is in
  `HB_TRUSTED_PROXIES` (CIDRs); the rightmost untrusted address wins.
- Serves the built SPA (`web/dist`, embedded): `/`, `/p/{id}` → `index.html`;
  hashed assets cached immutable; `sw.js` and `index.html` no-cache.
  `POST /share` (share-target fallback without a service worker) → `303 /`.
- Security headers on every response:
  `Content-Security-Policy: default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; manifest-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`,
  `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`,
  `Cross-Origin-Opener-Policy: same-origin`,
  `Permissions-Policy: camera=(), microphone=(), geolocation=()`.

### Configuration (environment)

| Variable | Default | Meaning |
|---|---|---|
| `HB_ADDR` | `:8080` | listen address |
| `HB_DATA_DIR` | `/data` | database + blobs |
| `HB_TRUSTED_PROXIES` | *(empty)* | comma-separated CIDRs |
| `HB_MAX_BODY` | `2097152` | paste body ciphertext bytes |
| `HB_MAX_COMMENT` | `262144` | comment ciphertext bytes |
| `HB_MAX_ATTACHMENT` | `26214400` | per attachment |
| `HB_MAX_PASTE_ATTACHMENTS` | `104857600` | total per paste |
| `HB_MAX_VERSIONS` | `200` | per paste / comment |
| `HB_ALLOW_NEVER` | `true` | allow non-expiring pastes |
| `HB_RATE_*` | see `server/README` | rate limits |

## 6. Client routes and storage

- `/` — new paste. `/p/<id>#<K>` — view. `/p/<id>#<K>.<ownerToken>` — owner
  link: the client stores the owner token and rewrites the URL to drop it.
- `localStorage` keys (all per paste id): owner token, guest author token,
  remembered `P`, nickname; plus UI preferences (theme, language, last
  paste type / expiry / discussion choice, editor mode).

## 7. Implementation notes

Choices the server makes where §4 is silent (the web client relies on them):

- Malformed IDs, unknown `/api` paths and wrong methods all return
  `404 not_found`.
- A missing `X-Read-Auth` is `403 unauthorized`; a malformed one counts as
  wrong. A present-but-wrong `X-Owner-Token` is always `403 forbidden`, even
  where the token is optional; when both owner and author tokens are sent,
  the owner wins.
- Burn pastes are readable only through `/open`. Versions, attachments and
  events return `400 invalid` before checking auth, so they cannot be used to
  test a password without burning the paste. Pastes without a discussion
  answer comment endpoints with `403 locked` / `404` before auth.
- Guests may delete (not create or edit) their own comments while the
  discussion is locked.
- Comment attachments may be uploaded before the comment exists (the client
  uploads, then posts). Attachments to tombstones are rejected.
- Envelopes must be at least 30 bytes and start with `0x01`; base64url
  decoding is strict.
- Extra limits: `HB_MAX_ATTACHMENT_META` (16 KiB), `HB_MAX_ATTACHMENT_COUNT`
  (500 per paste), `HB_MAX_COMMENTS` (1000 per paste),
  `HB_MAX_STREAMS_PER_IP` (16 concurrent event streams). IPv6 clients are
  rate-limited per /64.
- Event streams open with an immediate `: ping`.
- Mermaid source travels in a text node, not an attribute: DOMPurify drops
  attribute values containing `-->`.
