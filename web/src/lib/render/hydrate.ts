// Post-render DOM work for sanitized Markdown: decrypt attachments into
// <img>/file cards, render Mermaid diagrams, wire copy buttons.

import { sanitizeSvg, escapeHtml } from './markdown';

export type AttMeta = { name: string; type: string; size: number };
export type AttSource = {
  resolve(id: string): Promise<{ meta: AttMeta; url: string }>;
};

export type HydrateLabels = {
  copied: string;
  download: string;
  missing: string;
  diagramError: string;
};

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
