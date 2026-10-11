// Post-render DOM work for sanitized Markdown: decrypt attachments into
// <img>/file cards, render Mermaid diagrams, wire copy buttons.

import { sanitizeSvg, escapeHtml } from './markdown';

export type AttMeta = { name: string; type: string; size: number };
export type AttSource = {
  resolve(id: string): Promise<{ meta: AttMeta; url: string }>;
  /** Object URL for a remote image, fetched through the server's proxy. */
  external?(url: string, register?: boolean): Promise<string>;
};

export type HydrateLabels = {
  copied: string;
  download: string;
  missing: string;
  diagramError: string;
  /** Remote image in a paste that doesn't exist yet. */
  extPending: string;
  extFailed: string;
  extDisabled: string;
};

const RASTER = /^image\/(png|jpe?g|gif|webp|avif|bmp)$/i;

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/** Shown instead of a remote image that is not (yet) available. */
export function extFallback(url: string, alt: string, reason: string): HTMLElement {
  const a = document.createElement('a');
  a.className = 'ext-fallback';
  a.href = url;
  a.target = '_blank';
  a.rel = 'noopener noreferrer nofollow';
  a.title = url;
  a.innerHTML =
    `<span class="ext-icon" aria-hidden="true"></span>` +
    `<span class="ext-info"><span class="ext-alt">${escapeHtml(alt || hostOf(url))}</span>` +
    `<span class="ext-why">${escapeHtml(hostOf(url))} · ${escapeHtml(reason)}</span></span>`;
  return a;
}

/** The paste does not exist yet, so the server cannot fetch for it. */
export class ExternalPending extends Error {}
/** The operator turned the image proxy off. */
export class ExternalDisabled extends Error {}

export function extReason(e: unknown, labels: HydrateLabels): string {
  if (e instanceof ExternalPending) return labels.extPending;
  if (e instanceof ExternalDisabled) return labels.extDisabled;
  return labels.extFailed;
}

/** Load a remote image into `img`, or swap in a fallback card. */
export function loadExternal(img: HTMLImageElement, url: string, atts: AttSource | null, labels: HydrateLabels, onLoad?: () => void) {
  img.classList.add('loading');
  const fail = (e: unknown) => img.replaceWith(extFallback(url, img.alt, extReason(e, labels)));
  if (!atts?.external) {
    fail(new Error('no source'));
    return;
  }
  atts
    .external(url)
    .then((src) => {
      img.onload = () => {
        img.classList.remove('loading');
        onLoad?.();
      };
      img.onerror = () => fail(new Error('decode'));
      img.src = src;
    })
    .catch(fail);
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

let mermaidMod: typeof import('mermaid').default | null = null;
let mermaidTheme: string | null = null;
let mermaidSeq = 0;

export function isDark(): boolean {
  const attr = document.documentElement.dataset.theme;
  if (attr === 'dark') return true;
  if (attr === 'light') return false;
  return matchMedia('(prefers-color-scheme: dark)').matches;
}

async function mermaid() {
  if (!mermaidMod) mermaidMod = (await import('mermaid')).default;
  const theme = isDark() ? 'dark' : 'default';
  if (theme !== mermaidTheme) {
    mermaidMod.initialize({
      startOnLoad: false,
      securityLevel: 'strict',
      theme,
      htmlLabels: false,
      flowchart: { htmlLabels: false },
      fontFamily: 'ui-sans-serif, system-ui, sans-serif',
    });
    mermaidTheme = theme;
  }
  return mermaidMod;
}

export async function renderMermaid(src: string): Promise<string> {
  const m = await mermaid();
  const id = `hb-mmd-${++mermaidSeq}`;
  try {
    const { svg } = await m.render(id, src);
    return sanitizeSvg(svg);
  } finally {
    document.getElementById(id)?.remove();
    document.getElementById('d' + id)?.remove();
  }
}

export function fileCard(meta: AttMeta, url: string, labels: HydrateLabels): HTMLElement {
  const a = document.createElement('a');
  a.className = 'file-card';
  a.href = url;
  a.download = meta.name;
  a.innerHTML =
    `<span class="file-icon" aria-hidden="true"></span>` +
    `<span class="file-info"><span class="file-name">${escapeHtml(meta.name)}</span>` +
    `<span class="file-size">${formatBytes(meta.size)} · ${escapeHtml(labels.download)}</span></span>`;
  return a;
}

/**
 * Hydrate a container holding output of renderMarkdown(). Safe to call
 * repeatedly; already-hydrated nodes are skipped.
 */
export function hydrate(root: HTMLElement, atts: AttSource | null, labels: HydrateLabels): void {
  for (const img of root.querySelectorAll<HTMLImageElement>('img[data-att]:not([data-hb])')) {
    img.dataset.hb = '1';
    const id = img.dataset.att!;
    img.classList.add('loading');
    if (!atts) continue;
    atts
      .resolve(id)
      .then(({ url, meta }) => {
        // Only raster images render inline; anything else becomes a file card.
        if (!RASTER.test(meta.type)) {
          img.replaceWith(fileCard(meta, url, labels));
          return;
        }
        img.src = url;
        if (!img.alt) img.alt = meta.name;
        img.classList.remove('loading');
      })
      .catch(() => {
        img.classList.remove('loading');
        img.classList.add('missing');
        img.alt = labels.missing;
      });
  }

  for (const img of root.querySelectorAll<HTMLImageElement>('img[data-ext]:not([data-hb])')) {
    img.dataset.hb = '1';
    loadExternal(img, img.dataset.ext!, atts, labels);
  }

  for (const a of root.querySelectorAll<HTMLAnchorElement>('a[data-att]:not([data-hb])')) {
    a.dataset.hb = '1';
    const id = a.dataset.att!;
    a.removeAttribute('target');
    a.classList.add('loading');
    if (!atts) continue;
    atts
      .resolve(id)
      .then(({ url, meta }) => a.replaceWith(fileCard(meta, url, labels)))
      .catch(() => {
        a.classList.remove('loading');
        a.classList.add('missing');
        a.title = labels.missing;
      });
  }

  for (const block of root.querySelectorAll<HTMLElement>('.mermaid-block:not([data-hb])')) {
    block.dataset.hb = '1';
    const src = block.querySelector('.mermaid-src')?.textContent ?? '';
    renderMermaid(src)
      .then((svg) => {
        block.innerHTML = svg;
        block.classList.add('rendered');
      })
      .catch((e) => {
        const err = document.createElement('div');
        err.className = 'mermaid-error';
        err.textContent = `${labels.diagramError}: ${String((e as Error)?.message ?? e).split('\n')[0]}`;
        block.prepend(err);
      });
  }

  if (!root.dataset.hbCopy) {
    root.dataset.hbCopy = '1';
    root.addEventListener('click', (ev) => {
      const btn = (ev.target as HTMLElement).closest<HTMLButtonElement>('button[data-copy]');
      if (!btn) return;
      const code = btn.closest('.code-block')?.querySelector('code')?.textContent ?? '';
      navigator.clipboard.writeText(code).then(() => {
        const old = btn.textContent;
        btn.textContent = labels.copied;
        btn.classList.add('done');
        setTimeout(() => {
          btn.textContent = old;
          btn.classList.remove('done');
        }, 1200);
      });
    });
  }
}
