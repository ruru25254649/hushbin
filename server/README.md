# Hushbin server

The Go backend of Hushbin: a single static binary that stores **only
ciphertext**, verifiers (SHA-256 of client tokens), sizes and timestamps, and
serves the embedded Svelte SPA. The contract with the frontend is
[`../DESIGN.md`](../DESIGN.md) (§2, §4, §5); this file covers operating the
server.

```
cmd/hushbin/        main: config, store, HTTP server, graceful shutdown, `healthcheck`
server/config       HB_* environment parsing
server/store        SQLite (WAL) + attachment blobs on disk
server/api          HTTP handlers, middleware, SSE, maintenance loop
server/events       in-process pub/sub keyed by paste ID
server/ratelimit    per-IP token buckets
server/clientip     client IP extraction (trusted proxies / X-Forwarded-For)
server/static       SPA file serving and cache policy
web/embed.go        //go:embed all:dist (the Vite build output)
```

## Building and running

The binary embeds `web/dist`, so build the frontend first:

```sh
(cd web && npm ci && npm run build)
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" ./cmd/hushbin
HB_DATA_DIR=./data ./hushbin
```

Requires Go 1.24 (`GOTOOLCHAIN=local` works) and pure Go only — no cgo. The
only dependency is `modernc.org/sqlite` (pinned to v1.46.1, the last release
that builds with Go 1.24). `go vet ./...` and `go build ./cmd/hushbin` need
`web/dist/` to exist; the server packages do not:

```sh
go test -race ./server/...
```

Subcommands: `hushbin healthcheck` (GETs `/healthz` on `HB_ADDR`, exit 0/1 —
used by the container `HEALTHCHECK`, as distroless has no shell) and
`hushbin version`.

### Docker

```sh
docker compose up -d --build      # see ../docker-compose.yml
```

The image is `gcr.io/distroless/static-debian12:nonroot` running as UID
65532 with `/data` as a volume (pre-created with the right owner, so a fresh
named volume is writable). It works with a read-only root filesystem.

## Configuration

All configuration is via environment variables. Invalid values abort startup.

| Variable | Default | Meaning |
|---|---|---|
| `HB_ADDR` | `:8080` | listen address |
| `HB_DATA_DIR` | `/data` | database + blobs (must be on a case-sensitive filesystem) |
| `HB_TRUSTED_PROXIES` | *(empty)* | comma-separated CIDRs (or bare IPs) of reverse proxies whose `X-Forwarded-For` is honoured |
| `HB_MAX_BODY` | `2097152` | paste body ciphertext bytes (decoded) |
| `HB_MAX_COMMENT` | `262144` | comment ciphertext bytes (decoded) |
| `HB_MAX_ATTACHMENT` | `26214400` | per attachment data blob |
| `HB_MAX_PASTE_ATTACHMENTS` | `104857600` | total attachment data per paste |
| `HB_MAX_VERSIONS` | `200` | versions kept per paste / comment; further edits → `413` |
| `HB_ALLOW_NEVER` | `true` | allow non-expiring pastes (`expiresIn: 0`) |
| `HB_MAX_ATTACHMENT_META` | `16384` | attachment `meta` envelope bytes (decoded) |
| `HB_MAX_ATTACHMENT_COUNT` | `500` | attachments per paste → `413` |
| `HB_MAX_COMMENTS` | `1000` | comments per paste (tombstones included) → `413` |
| `HB_MAX_STREAMS_PER_IP` | `16` | concurrent `/events` streams per client → `429` |
| `HB_RATE_CREATE` | `60/h` | `POST /api/pastes` |
| `HB_RATE_COMMENT` | `60/10m` | comment create **and** edit |
| `HB_RATE_UPLOAD` | `120/10m` | `POST /api/pastes/{id}/attachments` |
| `HB_RATE_AUTH` | `30/10m` | **failed** read-auth / owner-token attempts |
| `HB_RATE_GENERAL` | `600/m` | every `/api/*` request |
| `HB_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `HB_LOG_FORMAT` | `text` | `text` or `json` (log/slog) |

`HB_MAX_ATTACHMENT_META`, `HB_MAX_ATTACHMENT_COUNT`, `HB_MAX_COMMENTS`,
`HB_MAX_STREAMS_PER_IP` and `HB_LOG_*` are additions to the DESIGN.md table;
the limits bound memory and disk use per paste and per client.

### Rate limits

Each `HB_RATE_*` value is `N/period`: a token bucket holding up to `N`
tokens that refills at `N` per `period`. `period` is a Go duration (`30s`,
`10m`, `1h30m`), a bare unit (`s`, `m`, `h`, `d`) or days (`7d`). `off` or
`0` disables the limit.

- Buckets are per client IP; IPv6 clients are grouped by `/64`.
- Exhausted buckets answer `429 {"error":"rate_limited"}` with a
  `Retry-After` header (seconds).
- `HB_RATE_AUTH` only counts **failures** (wrong/malformed `X-Read-Auth`,
  wrong `X-Owner-Token`). Once a client has used it up, *every* request that
  needs auth is refused until it refills — including correct passwords — so
  password guessing cannot continue. Successful requests never consume it.
- `HB_RATE_GENERAL` applies to `/api/*` only; the SPA, `/healthz` and
  `/share` are not limited.
- Idle buckets are evicted every minute.

### Client IP and trusted proxies

The peer address (`RemoteAddr`) is the client unless it lies in
`HB_TRUSTED_PROXIES`. Then `X-Forwarded-For` (all header lines, in order) is
walked right to left, skipping trusted addresses; the first untrusted one is
the client. Entries a client injects on the left are therefore ignored. A
malformed entry stops the walk (the last trusted hop is used). Without
`HB_TRUSTED_PROXIES`, `X-Forwarded-For` is ignored entirely.

When the proxy reaches hushbin through Docker's port publishing, the peer
hushbin sees is the Docker bridge gateway (e.g. `172.17.0.1`) — include it
(the compose example trusts `172.16.0.0/12`).

## Reverse proxy

Terminate TLS in front of hushbin and bind it to localhost
(`127.0.0.1:8080`). Requirements:

- **Event streams** (`GET /api/pastes/{id}/events`) must not be buffered.
  Hushbin sends `X-Accel-Buffering: no`, a `: ping` every 20 s and flushes
  each event; idle timeouts must exceed 20 s.
- **Body size**: a create request can carry the body plus up to
  `HB_MAX_PASTE_ATTACHMENTS` of attachments (≈ 103 MiB with the defaults,
  plus multipart overhead). Allow ~128 MiB, more if you raise the limits.
- **X-Forwarded-For** must be set, and the proxy's address listed in
  `HB_TRUSTED_PROXIES`.
- Do not compress `/api/*`: payloads are ciphertext (incompressible) and
  compression can delay event-stream flushes.
- Add `Strict-Transport-Security` at the proxy. Hushbin already sets CSP,
  `Referrer-Policy`, `X-Content-Type-Options`, COOP and `Permissions-Policy`.

### Caddy

```caddy
paste.example.com {
	@static not path /api/*
	encode @static zstd gzip

	request_body {
		max_size 128MB
	}

	header Strict-Transport-Security "max-age=31536000"

	# Caddy sets X-Forwarded-For itself and flushes text/event-stream
	# responses immediately.
	reverse_proxy 127.0.0.1:8080
}
```

with `HB_TRUSTED_PROXIES=127.0.0.1/32,::1/128` (Caddy on the host) or the
Docker bridge range (Caddy in a container).

### Nginx

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name paste.example.com;
    # ssl_certificate / ssl_certificate_key ...

    client_max_body_size 128m;   # >= HB_MAX_PASTE_ATTACHMENTS + body + overhead
    add_header Strict-Transport-Security "max-age=31536000" always;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_request_buffering off;     # stream large uploads to hushbin
        proxy_read_timeout 15m;
        proxy_send_timeout 15m;
    }

    # Live events: Server-Sent Events, never buffered.
    location ~ ^/api/pastes/[A-Za-z0-9]+/events$ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 1h;           # pings arrive every 20 s
    }
}
```

`$proxy_add_x_forwarded_for` appends the peer to whatever the client sent;
hushbin only trusts the rightmost untrusted entry, so that is safe.

## Data

```
$HB_DATA_DIR/hushbin.db        SQLite, WAL mode (+ -wal, -shm), mode 0600
$HB_DATA_DIR/att/<paste>/<id>  attachment envelopes (raw ciphertext)
$HB_DATA_DIR/tmp/              uploads in flight; cleaned at startup and hourly
```

Tables: `pastes`, `paste_versions`, `comments`, `comment_versions`,
`attachments`. Blobs are written to `tmp/`, fsynced and renamed into place;
deleting a paste, comment or expired paste removes them. At startup,
leftover temp files and blob directories without a paste are removed.

Backups: `sqlite3 hushbin.db ".backup backup.db"` plus a copy of `att/`
(or stop the container and copy the volume). Backups contain ciphertext
only; the keys live in URL fragments that never reach the server.

## Behaviour notes (beyond DESIGN.md)

These are the server's interpretations where DESIGN.md is silent:

- Malformed IDs in URLs (paste, comment, attachment, version number) are
  `404 not_found`, like unknown ones. Unknown `/api` paths and unsupported
  methods are `404 not_found` (JSON).
- A missing `X-Read-Auth` is `403 unauthorized`; a malformed one counts as a
  wrong one. A present-but-wrong `X-Owner-Token` is always
  `403 forbidden`, even on endpoints where it is optional. When both owner
  and author tokens are sent on comment create, the comment is the owner's.
- Burn pastes are only readable through `/open`. `versions`,
  `versions/{v}`, attachment download/upload and `events` answer
  `400 invalid` *before* checking read auth; comment endpoints on pastes
  without a discussion answer `403 locked` (create) or `404` (edit, delete,
  history) before read auth. None of them can be used to test a password
  without burning the paste.
- `POST /lock` on a paste without a discussion is `400 invalid`; every
  successful call emits `paste.locked`.
- While locked, guests can neither comment, reply, edit nor attach, but can
  still delete their own comments; the owner can do everything.
- Replying to a missing comment, a reply or a tombstone is `400 invalid`.
  Editing a tombstone is `400 invalid`; deleting it again or fetching its
  history is `404`.
- A comment attachment may be uploaded before its comment exists (clients
  generate comment IDs). Attaching to a tombstoned comment is `400`.
- Every envelope (content, meta, data) must be at least 30 bytes and start
  with version byte `0x01`, else `400 invalid`. b64u is decoded strictly
  (no padding, no whitespace, canonical).
- `kdf` must be `null`/absent or exactly `{"alg":"argon2id","m","t","p"}`
  with integers `8 ≤ m ≤ 4194304` (and `m ≥ 8p`), `1 ≤ t ≤ 64`,
  `1 ≤ p ≤ 255`. `burn`, `discussion` and `expiresIn` are required;
  `attachments` may be omitted.
- Duplicate attachment IDs inside one create request are `409 conflict`; an
  attachment part without a meta entry (or vice versa) or any unknown part is
  `400 invalid`.
- `409` for a stale `baseVersion` carries `"version"`; `409` for a duplicate
  client ID does not.
- Event streams start with an immediate `: ping`. If the paste vanishes
  between authentication and subscription the request is `404`.
- Open responses are streamed; comments are ordered by creation time,
  attachments by upload time. Burn opens inline every attachment's `data`.
- API responses carry `Cache-Control: no-store` (events: `no-cache`).
- Expired pastes are `404` everywhere immediately and physically removed by
  the sweep (every minute), which also emits `paste.deleted`.
