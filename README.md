# Hushbin

A self-hosted, end-to-end encrypted clipboard. Paste text, Markdown, images or
files, get a link, and share it. The server only ever stores ciphertext.

- **Two paste types.** *Keep* stays until it expires (5 min – 1 month, or
  never), can be edited with full version history, and can have a discussion.
  *Send once* is destroyed the moment it is opened.
- **Real Markdown.** GFM tables, task lists, KaTeX math, Mermaid diagrams and
  highlighted code. The editor has an Obsidian-style live preview, and source
  mode is one click (or <kbd>Ctrl</kbd>+<kbd>E</kbd>) away.
- **Images and files.** Paste a screenshot, drop a PDF. Encrypted in the
  browser like everything else (25 MiB per file by default).
- **Anonymous discussion.** Optional nickname, an *Author* badge for the
  paste owner, one level of replies, live updates, editable messages with
  history, and owner moderation (delete, lock).
- **No accounts.** The browser that creates something keeps a secret that lets
  it edit or delete it. *Copy owner link* carries that secret to your other
  devices.
- **Installable** (PWA, including “share to Hushbin” on phones), light/dark,
  keyboard driven (<kbd>Ctrl</kbd>+<kbd>K</kbd> command palette, <kbd>?</kbd>
  for shortcuts), and in English, 日本語, 简体中文 and 繁體中文.

## Security model

```
/p/<id>#<key>                 share link: the key never leaves the browser
/p/<id>#<key>.<ownerToken>    owner link: for your own devices only
```

- A random 256-bit key lives in the URL fragment, which browsers never send
  to the server. An optional password is stretched with Argon2id and mixed
  into the key, so a leaked link alone is not enough to read a passworded
  paste.
- Content, titles, nicknames, file names and file contents are encrypted with
  AES-256-GCM in the browser. The server sees sizes, timestamps, and which
  messages belong to which paste.
- Reads are gated: the browser derives a *read token* from the key (and
  password) and the server only returns ciphertext to callers presenting it.
  Password guesses therefore go through the server's rate limiter instead of
  being tried offline, and a wrong password never burns a *Send once* paste.
- *Keep* pastes ask for a password by default. You can switch it off for a
  single paste after a warning.
- Rendered Markdown is sanitized, raw HTML is never rendered, remote images
  are not loaded (they would leak the reader's IP), and a strict Content
  Security Policy blocks inline scripts.

What it does **not** protect against: like every web-based E2E tool, you
trust the server to deliver honest JavaScript. If the server is compromised,
the next page load can be too. Run it on a machine you control.

See [DESIGN.md](DESIGN.md) for the exact cryptography and API.

## Run it

```sh
docker compose up -d          # listens on 127.0.0.1:8080
```

Put it behind your reverse proxy for TLS. A secure context (HTTPS or
localhost) is required: browsers only expose WebCrypto there. Proxy
snippets for Caddy and Nginx, plus every configuration variable, are in
[server/README.md](server/README.md).

Back up the `/data` volume (SQLite database plus encrypted blobs) to back up
everything.

## Develop

```sh
# The Go binary embeds web/dist, so build the frontend once first
cd web && npm install && npm run build && cd ..
HB_DATA_DIR=./data go run ./cmd/hushbin     # API + app on :8080

# Frontend with hot reload on :5173, proxying /api to :8080
cd web && npm run dev
```

Checks:

```sh
go vet ./... && go test -race ./server/...
cd web && npm run check && npm test
# end-to-end, against a running server:
cd web && HB_URL=http://127.0.0.1:8080 npx playwright test
```

## Layout

```
cmd/hushbin/      entry point
server/           Go API: storage (SQLite), auth, rate limits, SSE, static files
web/              Svelte 5 + Vite frontend
  src/lib/        crypto, API client, Markdown renderer, CodeMirror live preview
  src/routes/     new paste / view paste screens
  src/components/ discussion, history/diff, dialogs, editor wrapper
  e2e/            Playwright end-to-end tests
DESIGN.md         product rules, cryptography, API contract
```
