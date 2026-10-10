// Hushbin service worker: offline app shell + Web Share Target inbox.
// Never caches API responses (they are ciphertext, but caching them would
// keep destroyed pastes around on the device).

const SHELL = 'hb-shell-v1';
const SHARE = 'hb-share-inbox';

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(SHELL).then((c) => c.addAll(['/', '/manifest.webmanifest', '/icon.svg'])).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== SHELL && k !== SHARE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

async function handleShare(request) {
  const form = await request.formData();
  const cache = await caches.open(SHARE);
  const files = [];
  let i = 0;
  for (const f of form.getAll('files')) {
    if (!(f instanceof File) || !f.size) continue;
    const key = `/__share/file-${i++}`;
    await cache.put(key, new Response(f, { headers: { 'Content-Type': f.type || 'application/octet-stream' } }));
    files.push({ key, name: f.name, type: f.type });
  }
  const meta = {
    title: String(form.get('title') || ''),
    text: String(form.get('text') || ''),
    url: String(form.get('url') || ''),
    files,
  };
  await cache.put('/__share/meta', new Response(JSON.stringify(meta), { headers: { 'Content-Type': 'application/json' } }));
  return Response.redirect('/?share=1', 303);
}

self.addEventListener('fetch', (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (url.origin !== location.origin) return;

  if (req.method === 'POST' && url.pathname === '/share') {
    e.respondWith(handleShare(req));
    return;
  }
  if (req.method !== 'GET' || url.pathname.startsWith('/api/')) return;

  // Hashed assets never change: cache first.
  if (url.pathname.startsWith('/assets/')) {
    e.respondWith(
      caches.open(SHELL).then(async (c) => {
        const hit = await c.match(req);
        if (hit) return hit;
        const res = await fetch(req);
        if (res.ok) c.put(req, res.clone());
        return res;
      }),
    );
    return;
  }

  // Navigations: network first, fall back to the cached shell when offline.
  if (req.mode === 'navigate') {
    e.respondWith(
      fetch(req)
        .then((res) => {
          if (res.ok) {
            const copy = res.clone();
            caches.open(SHELL).then((c) => c.put('/', copy));
          }
          return res;
        })
        .catch(() => caches.match('/')),
    );
  }
});
