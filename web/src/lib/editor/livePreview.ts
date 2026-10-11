// Obsidian-style "live preview" for CodeMirror 6: Markdown syntax is hidden
// and rendered in place, except around the cursor, where the source shows
// so it can be edited. Block constructs (tables, math, Mermaid, images) are
// rendered as widgets; clicking one moves the cursor into its source.

import { Facet, StateField, type EditorState, type Range, type Extension } from '@codemirror/state';
import { Decoration, EditorView, WidgetType, type DecorationSet } from '@codemirror/view';
import { syntaxTree } from '@codemirror/language';
import type { SyntaxNodeRef } from '@lezer/common';
import { ensureDeps, renderMarkdownSync, renderMath, escapeHtml, normalizeExt } from '../render/markdown';
import { hydrate, renderMermaid, fileCard, loadExternal, type AttSource, type HydrateLabels } from '../render/hydrate';

export type LiveContext = {
  atts: AttSource | null;
  labels: HydrateLabels & { copy: string };
};

const defaultCtx: LiveContext = {
  atts: null,
  labels: {
    copy: 'Copy',
    copied: 'Copied',
    download: 'Download',
    missing: 'Missing attachment',
    diagramError: 'Diagram error',
    extPending: 'Remote image',
    extFailed: 'Could not load',
    extDisabled: 'Remote images are off',
  },
};

export const liveContext = Facet.define<LiveContext, LiveContext>({
  combine: (v) => (v.length ? v[v.length - 1] : defaultCtx),
});

/** Live preview is disabled above this size to keep typing fast. */
export const LIVE_MAX_CHARS = 200_000;

// ------------------------------------------------------------------ widgets

function placeCursor(view: EditorView, dom: HTMLElement, ev: MouseEvent) {
  if ((ev.target as HTMLElement).closest('a[href], button, input')) return;
  ev.preventDefault();
  const pos = view.posAtDOM(dom);
  view.dispatch({ selection: { anchor: pos } });
  view.focus();
}

class BulletWidget extends WidgetType {
  eq() {
    return true;
  }
  toDOM() {
    const s = document.createElement('span');
    s.className = 'cm-lp-bullet';
    s.textContent = '•';
    return s;
  }
}

class CheckboxWidget extends WidgetType {
  constructor(readonly checked: boolean) {
    super();
  }
  eq(o: CheckboxWidget) {
    return o.checked === this.checked;
  }
  toDOM(view: EditorView) {
    const box = document.createElement('input');
    box.type = 'checkbox';
    box.className = 'cm-lp-task';
    box.checked = this.checked;
    box.addEventListener('mousedown', (e) => e.preventDefault());
    box.addEventListener('click', (e) => {
      e.preventDefault();
      const pos = view.posAtDOM(box);
      const ch = view.state.sliceDoc(pos + 1, pos + 2);
      view.dispatch({ changes: { from: pos + 1, to: pos + 2, insert: ch === ' ' ? 'x' : ' ' } });
    });
    return box;
  }
  ignoreEvent() {
    return true;
  }
}

class HrWidget extends WidgetType {
  eq() {
    return true;
  }
  toDOM() {
    const s = document.createElement('span');
    s.className = 'cm-lp-hr';
    return s;
  }
}

class InlineMathWidget extends WidgetType {
  constructor(readonly tex: string) {
    super();
  }
  eq(o: InlineMathWidget) {
    return o.tex === this.tex;
  }
  toDOM(view: EditorView) {
    const s = document.createElement('span');
    s.className = 'cm-lp-math';
    s.textContent = this.tex;
    ensureDeps('$').then(() => {
      s.innerHTML = renderMath(this.tex, false);
      view.requestMeasure();
    });
    s.addEventListener('mousedown', (e) => placeCursor(view, s, e));
    return s;
  }
  ignoreEvent() {
    return true;
  }
}

class ImageWidget extends WidgetType {
  constructor(
    readonly src: string,
    readonly alt: string,
  ) {
    super();
  }
  eq(o: ImageWidget) {
    return o.src === this.src && o.alt === this.alt;
  }
  get estimatedHeight() {
    return 200;
  }
  toDOM(view: EditorView) {
    const wrap = document.createElement('span');
    wrap.className = 'cm-lp-image';
    if (this.src.startsWith('att:')) {
      const img = document.createElement('img');
      img.alt = this.alt;
      img.className = 'loading';
      wrap.appendChild(img);
      const ctx = view.state.facet(liveContext);
      ctx.atts
        ?.resolve(this.src.slice(4))
        .then(({ url }) => {
          img.src = url;
          img.classList.remove('loading');
          img.onload = () => view.requestMeasure();
        })
        .catch(() => {
          img.classList.replace('loading', 'missing');
          img.alt = ctx.labels.missing;
        });
    } else {
      // Remote image: through the server's proxy, never fetched directly.
      const img = document.createElement('img');
      img.alt = this.alt;
      wrap.appendChild(img);
      const ctx = view.state.facet(liveContext);
      const url = normalizeExt(this.src);
      if (url) loadExternal(img, url, ctx.atts, ctx.labels, () => view.requestMeasure());
      else img.replaceWith(document.createTextNode(this.alt || this.src));
    }
    wrap.addEventListener('mousedown', (e) => placeCursor(view, wrap, e));
    return wrap;
  }
  ignoreEvent() {
    return true;
  }
}

class FileWidget extends WidgetType {
  constructor(
    readonly id: string,
    readonly label: string,
  ) {
    super();
  }
  eq(o: FileWidget) {
    return o.id === this.id && o.label === this.label;
  }
  toDOM(view: EditorView) {
    const wrap = document.createElement('span');
    wrap.className = 'cm-lp-file';
    wrap.textContent = this.label;
    const ctx = view.state.facet(liveContext);
    ctx.atts
      ?.resolve(this.id)
      .then(({ meta, url }) => {
        wrap.textContent = '';
        wrap.appendChild(fileCard(meta, url, ctx.labels));
        view.requestMeasure();
      })
      .catch(() => wrap.classList.add('missing'));
    wrap.addEventListener('mousedown', (e) => placeCursor(view, wrap, e));
    return wrap;
  }
  ignoreEvent() {
    return true;
  }
}

/** Block rendered through the shared Markdown renderer (tables, block math). */
class BlockWidget extends WidgetType {
  constructor(
    readonly kind: 'table' | 'math' | 'mermaid',
    readonly source: string,
  ) {
    super();
  }
  eq(o: BlockWidget) {
    return o.kind === this.kind && o.source === this.source;
  }
  get estimatedHeight() {
    return this.kind === 'mermaid' ? 240 : 60;
  }
  toDOM(view: EditorView) {
    const div = document.createElement('div');
    div.className = `cm-lp-block cm-lp-${this.kind} markdown`;
    const ctx = view.state.facet(liveContext);
    if (this.kind === 'mermaid') {
      div.innerHTML = `<pre class="mermaid-src">${escapeHtml(this.source)}</pre>`;
      renderMermaid(this.source)
        .then((svg) => {
          div.innerHTML = svg;
          view.requestMeasure();
        })
        .catch((e) => {
          const err = document.createElement('div');
          err.className = 'mermaid-error';
          err.textContent = `${ctx.labels.diagramError}: ${String((e as Error)?.message ?? e).split('\n')[0]}`;
          div.prepend(err);
          view.requestMeasure();
        });
    } else {
      const fill = () => {
        div.innerHTML = renderMarkdownSync(this.source, ctx.labels);
        hydrate(div, ctx.atts, ctx.labels);
        view.requestMeasure();
      };
      if (this.kind === 'math' || this.source.includes('$')) {
        div.textContent = this.source;
        ensureDeps(this.source).then(fill);
      } else {
        fill();
      }
    }
    div.addEventListener('mousedown', (e) => placeCursor(view, div, e));
    return div;
  }
  ignoreEvent() {
    return true;
  }
}

// ------------------------------------------------------------------ builder

const hide = Decoration.replace({});
const mark = (cls: string) => Decoration.mark({ class: cls });
const line = (cls: string) => Decoration.line({ class: cls });

const MARK_CLASS: Record<string, string> = {
  Emphasis: 'cm-lp-em',
  StrongEmphasis: 'cm-lp-strong',
  Strikethrough: 'cm-lp-strike',
  InlineCode: 'cm-lp-code',
};
const MARK_CHILD: Record<string, string> = {
  Emphasis: 'EmphasisMark',
  StrongEmphasis: 'EmphasisMark',
  Strikethrough: 'StrikethroughMark',
  InlineCode: 'CodeMark',
};

function build(state: EditorState): DecorationSet {
  const doc = state.doc;
  const ranges = state.selection.ranges;
  const out: Range<Decoration>[] = [];

  const touches = (from: number, to: number) => ranges.some((r) => r.from <= to && r.to >= from);
  const linesTouched = (from: number, to: number) => {
    const a = doc.lineAt(from).number;
    const b = doc.lineAt(to).number;
    return ranges.some((r) => doc.lineAt(r.from).number <= b && doc.lineAt(r.to).number >= a);
  };
  const hideWithSpace = (from: number, to: number) => {
    const next = doc.sliceString(to, to + 1);
    out.push(hide.range(from, next === ' ' ? to + 1 : to));
  };
  const eachLine = (from: number, to: number, f: (l: { from: number; number: number }, first: boolean, last: boolean) => void) => {
    const a = doc.lineAt(from).number;
    const b = doc.lineAt(to).number;
    for (let n = a; n <= b; n++) f(doc.line(n), n === a, n === b);
  };
  const blockRange = (from: number, to: number) => ({ from: doc.lineAt(from).from, to: doc.lineAt(to).to });

  syntaxTree(state).iterate({
    enter(n: SyntaxNodeRef): boolean | void {
      const name = n.name;
      const node = n.node;

      if (name.startsWith('ATXHeading')) {
        const lvl = Number(name.slice(-1));
        out.push(line(`cm-lp-h cm-lp-h${lvl}`).range(doc.lineAt(n.from).from));
        if (!touches(n.from, n.to)) {
          const m = node.getChild('HeaderMark');
          if (m && m.from === n.from) hideWithSpace(m.from, m.to);
          // closing #'s
          const marks = node.getChildren('HeaderMark');
          if (marks.length > 1) {
            const last = marks[marks.length - 1];
            out.push(hide.range(last.from, last.to));
          }
        }
        return;
      }
      if (name.startsWith('SetextHeading')) {
        const lvl = Number(name.slice(-1));
        out.push(line(`cm-lp-h cm-lp-h${lvl}`).range(doc.lineAt(n.from).from));
        return;
      }

      if (name in MARK_CLASS) {
        if (n.to > n.from) out.push(mark(MARK_CLASS[name]).range(n.from, n.to));
        if (!touches(n.from, n.to)) {
          for (const m of node.getChildren(MARK_CHILD[name])) out.push(hide.range(m.from, m.to));
        }
        return name === 'InlineCode' ? false : undefined;
      }

      if (name === 'Escape') {
        if (!touches(n.from, n.to)) out.push(hide.range(n.from, n.from + 1));
        return false;
      }

      if (name === 'Image') {
        if (touches(n.from, n.to)) return;
        const url = node.getChild('URL');
        const marks = node.getChildren('LinkMark');
        const alt = marks.length >= 2 ? doc.sliceString(marks[0].to, marks[1].from) : '';
        if (url) {
          out.push(Decoration.replace({ widget: new ImageWidget(doc.sliceString(url.from, url.to), alt) }).range(n.from, n.to));
          return false;
        }
        return;
      }

      if (name === 'Link') {
        const url = node.getChild('URL');
        const href = url ? doc.sliceString(url.from, url.to) : '';
        const marks = node.getChildren('LinkMark');
        if (marks.length < 2) return;
        const textFrom = marks[0].to;
        const textTo = marks[1].from;
        if (textTo > textFrom) out.push(mark('cm-lp-link').range(textFrom, textTo));
        if (touches(n.from, n.to)) return;
        if (href.startsWith('att:')) {
          out.push(Decoration.replace({ widget: new FileWidget(href.slice(4), doc.sliceString(textFrom, textTo)) }).range(n.from, n.to));
          return false;
        }
        out.push(hide.range(marks[0].from, marks[0].to));
        out.push(hide.range(marks[1].from, n.to));
        return;
      }

      if (name === 'URL' || name === 'Autolink') {
        const p = node.parent?.name;
        if (p !== 'Link' && p !== 'Image') out.push(mark('cm-lp-url').range(n.from, n.to));
        return false;
      }

      if (name === 'Blockquote') {
        eachLine(n.from, n.to, (l) => out.push(line('cm-lp-quote').range(l.from)));
        return;
      }
      if (name === 'QuoteMark') {
        if (!linesTouched(n.from, n.to)) hideWithSpace(n.from, n.to);
        return;
      }

      if (name === 'ListMark') {
        const item = node.parent;
        const list = item?.parent;
        if (linesTouched(n.from, n.to)) return;
        if (list?.name === 'BulletList') {
          const isTask = item?.getChild('Task');
          if (isTask) hideWithSpace(n.from, n.to);
          else out.push(Decoration.replace({ widget: new BulletWidget() }).range(n.from, n.to));
        } else {
          out.push(mark('cm-lp-olmark').range(n.from, n.to));
        }
        return;
      }

      if (name === 'TaskMarker') {
        const checked = /x/i.test(doc.sliceString(n.from, n.to));
        if (checked) {
          const task = node.parent;
          if (task && n.to + 1 < task.to) out.push(mark('cm-lp-done').range(n.to + 1, task.to));
        }
        if (!touches(n.from, n.to)) out.push(Decoration.replace({ widget: new CheckboxWidget(checked) }).range(n.from, n.to));
        return;
      }

      if (name === 'HorizontalRule') {
        if (!linesTouched(n.from, n.to)) {
          out.push(Decoration.replace({ widget: new HrWidget() }).range(n.from, n.to));
        }
        return false;
      }

      if (name === 'FencedCode' || name === 'CodeBlock') {
        const info = node.getChild('CodeInfo');
        const lang = info ? doc.sliceString(info.from, info.to).trim().toLowerCase() : '';
        if (lang === 'mermaid' && !linesTouched(n.from, n.to)) {
          const text = node.getChild('CodeText');
          const src = text ? doc.sliceString(text.from, text.to) : '';
          const r = blockRange(n.from, n.to);
          out.push(Decoration.replace({ widget: new BlockWidget('mermaid', src), block: true }).range(r.from, r.to));
          return false;
        }
        const fenced = name === 'FencedCode';
        eachLine(n.from, n.to, (l, first, last) => {
          let cls = 'cm-lp-codeblock';
          if (first) cls += ' cm-lp-codeblock-first';
          if (last) cls += ' cm-lp-codeblock-last';
          if (fenced && (first || (last && doc.lineAt(n.to).text.trim().match(/^(`{3,}|~{3,})$/)))) cls += ' cm-lp-fence';
          out.push(line(cls).range(l.from));
        });
        return false;
      }

      if (name === 'BlockMath') {
        if (!linesTouched(n.from, n.to)) {
          const r = blockRange(n.from, n.to);
          out.push(Decoration.replace({ widget: new BlockWidget('math', doc.sliceString(r.from, r.to)), block: true }).range(r.from, r.to));
        } else {
          eachLine(n.from, n.to, (l) => out.push(line('cm-lp-mathsrc').range(l.from)));
        }
        return false;
      }

      if (name === 'InlineMath') {
        if (!touches(n.from, n.to)) {
          out.push(Decoration.replace({ widget: new InlineMathWidget(doc.sliceString(n.from + 1, n.to - 1)) }).range(n.from, n.to));
        } else {
          out.push(mark('cm-lp-mathsrc-inline').range(n.from, n.to));
        }
        return false;
      }

      if (name === 'Table') {
        if (!linesTouched(n.from, n.to)) {
          const r = blockRange(n.from, n.to);
          out.push(Decoration.replace({ widget: new BlockWidget('table', doc.sliceString(r.from, r.to)), block: true }).range(r.from, r.to));
        } else {
          eachLine(n.from, n.to, (l) => out.push(line('cm-lp-tablesrc').range(l.from)));
        }
        return false;
      }
    },
  });

  return Decoration.set(out, true);
}

const livePreviewField = StateField.define<DecorationSet>({
  create: (state) => build(state),
  update(deco, tr) {
    if (tr.docChanged || tr.selection || syntaxTree(tr.state) !== syntaxTree(tr.startState)) {
      return build(tr.state);
    }
    return deco;
  },
  provide: (f) => EditorView.decorations.from(f),
});

export function livePreview(): Extension {
  return [livePreviewField, EditorView.editorAttributes.of({ class: 'cm-live' })];
}
