// Markdown → sanitized HTML. GFM (tables, strikethrough, task lists,
// autolinks), KaTeX math, highlighted code, Mermaid placeholders, and
// `att:<id>` references to encrypted attachments.
//
// Raw HTML in the source is never rendered; the output still goes through
// DOMPurify because a script running on this origin could read the key from
// the URL fragment.

import MarkdownIt, { type StateBlock, type StateInline, type StateCore } from 'markdown-it';
import DOMPurify from 'dompurify';
import { findInlineMathEnd } from '../editor/math';

type Katex = typeof import('katex').default;
type Hljs = typeof import('highlight.js').default;

let katex: Katex | null = null;
let hljs: Hljs | null = null;
let katexLoading: Promise<void> | null = null;
let hljsLoading: Promise<void> | null = null;

/** Load the optional renderers this source needs. */
export async function ensureDeps(src: string): Promise<void> {
  const jobs: Promise<void>[] = [];
  if (!katex && src.includes('$')) {
    katexLoading ??= Promise.all([import('katex'), import('katex/dist/katex.min.css')]).then(([k]) => {
      katex = k.default;
    });
    jobs.push(katexLoading);
  }
  if (!hljs && /(^|\n)\s*(```|~~~)/.test(src)) {
    hljsLoading ??= import('highlight.js/lib/common').then((h) => {
      hljs = h.default;
    });
    jobs.push(hljsLoading);
  }
  await Promise.all(jobs);
}

export function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
}

export function renderMath(tex: string, display: boolean): string {
  if (!katex) return `<code class="math-fallback">${escapeHtml(tex)}</code>`;
  return katex.renderToString(tex, { displayMode: display, throwOnError: false, trust: false, strict: 'ignore', output: 'htmlAndMathml' });
}

// ------------------------------------------------------------ math rules

function mathInline(state: StateInline, silent: boolean): boolean {
  const src = state.src;
  const pos = state.pos;
  if (src.charCodeAt(pos) !== 0x24) return false;
  if (src.charCodeAt(pos + 1) === 0x24) return false;
  if (pos > 0 && src.charCodeAt(pos - 1) === 0x24) return false;
  const close = findInlineMathEnd((i) => src.charCodeAt(i), pos, state.posMax);
  if (close < 0) return false;
  if (!silent) {
    const tok = state.push('math_inline', 'math', 0);
    tok.markup = '$';
    tok.content = src.slice(pos + 1, close);
  }
  state.pos = close + 1;
  return true;
}

function mathBlock(state: StateBlock, startLine: number, endLine: number, silent: boolean): boolean {
  if (state.sCount[startLine] - state.blkIndent >= 4) return false;
  const start = state.bMarks[startLine] + state.tShift[startLine];
  const max = state.eMarks[startLine];
  if (start + 2 > max || state.src.charCodeAt(start) !== 0x24 || state.src.charCodeAt(start + 1) !== 0x24) return false;
  if (silent) return true;

  const first = state.src.slice(start + 2, max).trimEnd();
  let content: string;
  let next = startLine + 1;
  if (first.length >= 2 && first.endsWith('$$')) {
    content = first.slice(0, -2);
  } else {
    const lines = first ? [first] : [];
    for (; next < endLine; next++) {
      const s = state.bMarks[next] + state.tShift[next];
      const e = state.eMarks[next];
      if (s < e && state.sCount[next] < state.blkIndent) break;
      const text = state.src.slice(s, e).trimEnd();
      if (text.endsWith('$$')) {
        if (text.length > 2) lines.push(text.slice(0, -2));
        next++;
        break;
      }
      lines.push(state.src.slice(Math.min(s, state.bMarks[next] + state.blkIndent), e));
    }
    content = lines.join('\n');
  }
  state.line = next;
  const tok = state.push('math_block', 'math', 0);
  tok.block = true;
  tok.content = content;
  tok.markup = '$$';
  tok.map = [startLine, next];
  return true;
}

// ------------------------------------------------------------ task lists

function taskLists(state: StateCore): void {
  const toks = state.tokens;
  for (let i = 2; i < toks.length; i++) {
    const t = toks[i];
    if (t.type !== 'inline' || toks[i - 1].type !== 'paragraph_open' || toks[i - 2].type !== 'list_item_open') continue;
    const m = /^\[([ xX])\](?:[ \t]|$)/.exec(t.content);
    if (!m || !t.children) continue;
    let strip = m[0].length;
    while (strip > 0 && t.children.length && t.children[0].type === 'text') {
      const c = t.children[0];
      if (c.content.length <= strip) {
        strip -= c.content.length;
        t.children.shift();
      } else {
        c.content = c.content.slice(strip);
        strip = 0;
      }
    }
    const cb = new state.Token('html_inline', '', 0);
    cb.content = `<input type="checkbox" class="task-box" disabled${m[1] === ' ' ? '' : ' checked'}>`;
    t.children.unshift(cb);
    toks[i - 2].attrJoin('class', 'task-item');
    for (let j = i - 3; j >= 0; j--) {
      if ((toks[j].type === 'bullet_list_open' || toks[j].type === 'ordered_list_open') && toks[j].level === toks[i - 2].level - 1) {
        if (!String(toks[j].attrGet('class') ?? '').includes('task-list')) toks[j].attrJoin('class', 'task-list');
        break;
      }
    }
  }
}

// ------------------------------------------------------------ instance

export type RenderLabels = { copy: string };

const md = new MarkdownIt({ html: false, linkify: true, breaks: true, typographer: false });
md.inline.ruler.after('escape', 'math_inline', mathInline);
md.block.ruler.before('fence', 'math_block', mathBlock, { alt: ['paragraph', 'reference', 'blockquote', 'list'] });
md.core.ruler.push('task_lists', taskLists);

md.renderer.rules.math_inline = (tokens, idx) => renderMath(tokens[idx].content, false);
md.renderer.rules.math_block = (tokens, idx) => `<div class="math-block">${renderMath(tokens[idx].content, true)}</div>\n`;

let labels: RenderLabels = { copy: 'Copy' };

function codeBlock(code: string, lang: string): string {
  if (lang === 'mermaid') {
    return `<div class="mermaid-block" data-mermaid="${escapeHtml(code)}"><pre class="mermaid-src">${escapeHtml(code)}</pre></div>\n`;
  }
  let html = escapeHtml(code);
  if (hljs && lang && hljs.getLanguage(lang)) {
    try {
      html = hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
    } catch {
      /* fall back to plain */
    }
  }
  const n = code.replace(/\n$/, '').split('\n').length;
  const nums = Array.from({ length: n }, (_, i) => i + 1).join('\n');
  return (
    `<div class="code-block"><div class="code-head"><span class="code-lang">${escapeHtml(lang)}</span>` +
    `<button type="button" class="code-copy" data-copy="1">${escapeHtml(labels.copy)}</button></div>` +
    `<div class="code-body"><pre class="code-ln" aria-hidden="true">${nums}</pre>` +
    `<pre class="code-pre"><code class="hljs${lang ? ' language-' + escapeHtml(lang) : ''}">${html}</code></pre></div></div>\n`
  );
}

md.renderer.rules.fence = (tokens, idx) => {
  const tok = tokens[idx];
  const lang = tok.info.trim().split(/\s+/)[0]?.toLowerCase() ?? '';
  return codeBlock(tok.content, lang);
};
md.renderer.rules.code_block = (tokens, idx) => codeBlock(tokens[idx].content, '');

md.renderer.rules.image = (tokens, idx, options, env, self) => {
  const tok = tokens[idx];
  const src = String(tok.attrGet('src') ?? '');
  const alt = self.renderInlineAsText(tok.children ?? [], options, env);
  if (src.startsWith('att:')) {
    return `<img class="att-img" data-att="${escapeHtml(src.slice(4))}" alt="${escapeHtml(alt)}">`;
  }
  // Remote images are not loaded (they would leak the reader's IP); show a link.
  return `<a href="${escapeHtml(src)}" class="ext-img">${escapeHtml(alt || src)}</a>`;
};

const defaultLinkOpen = md.renderer.rules.link_open ?? ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options));
md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  const tok = tokens[idx];
  const href = String(tok.attrGet('href') ?? '');
  if (href.startsWith('att:')) {
    tok.attrs = [
      ['class', 'att-link'],
      ['data-att', href.slice(4)],
    ];
  }
  return defaultLinkOpen(tokens, idx, options, env, self);
};

DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A' && node.hasAttribute('href')) {
    node.setAttribute('target', '_blank');
    node.setAttribute('rel', 'noopener noreferrer nofollow');
  }
});

export function sanitize(html: string): string {
  return DOMPurify.sanitize(html, {
    FORBID_TAGS: ['style', 'form', 'iframe', 'object', 'embed'],
    FORBID_ATTR: ['srcset', 'action', 'formaction'],
    ADD_ATTR: ['target'],
  });
}

/** Render Markdown to sanitized HTML. Call `ensureDeps(src)` first for math/code. */
export function renderMarkdownSync(src: string, l?: RenderLabels): string {
  if (l) labels = l;
  return sanitize(md.render(src));
}

export async function renderMarkdown(src: string, l?: RenderLabels): Promise<string> {
  await ensureDeps(src);
  return renderMarkdownSync(src, l);
}

export function sanitizeSvg(svg: string): string {
  return DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true }, ADD_TAGS: ['style'] });
}
