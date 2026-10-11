// Web Share Target inbox. The service worker receives POST /share, stashes
// the shared text and files in Cache Storage, and redirects to /?share=1.

const CACHE = 'hb-share-inbox';

export type Shared = { title: string; text: string; files: File[] };

export async function takeShared(): Promise<Shared | null> {
  if (!('caches' in window)) return null;
  try {
    const cache = await caches.open(CACHE);
    const metaRes = await cache.match('/__share/meta');
    if (!metaRes) return null;
    const meta = (await metaRes.json()) as { title: string; text: string; url: string; files: { key: string; name: string; type: string }[] };
    const files: File[] = [];
    for (const f of meta.files) {
      const r = await cache.match(f.key);
      if (r) files.push(new File([await r.blob()], f.name, { type: f.type }));
    }
    await caches.delete(CACHE);
    const text = [meta.text, meta.url].filter(Boolean).join('\n');
    return { title: meta.title ?? '', text, files };
  } catch {
    return null;
  }
}
