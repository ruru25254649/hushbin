// CodeMirror setup shared by the paste editor and the comment composer.

import { Compartment, EditorState, type Extension } from '@codemirror/state';
import { EditorView, keymap, placeholder as cmPlaceholder, drawSelection, highlightActiveLine } from '@codemirror/view';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { syntaxHighlighting, HighlightStyle, indentOnInput, bracketMatching } from '@codemirror/language';
import { markdown, markdownLanguage, markdownKeymap } from '@codemirror/lang-markdown';
import { languages } from '@codemirror/language-data';
import { searchKeymap, highlightSelectionMatches } from '@codemirror/search';
import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
import { tags as t } from '@lezer/highlight';
import { MathExtension } from './math';
import { livePreview, liveContext, LIVE_MAX_CHARS, type LiveContext } from './livePreview';

export type EditorMode = 'live' | 'source';

export type EditorOptions = {
  doc: string;
  mode: EditorMode;
  placeholder?: string;
  context: LiveContext;
  autofocus?: boolean;
  minLines?: number;
  onChange?: (doc: string) => void;
  onSubmit?: () => void;
  onToggleMode?: () => void;
  /** Encrypts/stages files and returns Markdown to insert for each. */
  onFiles?: (files: File[]) => Promise<string[]>;
};

const highlight = HighlightStyle.define([
  { tag: t.heading, fontWeight: '650' },
  { tag: t.strong, fontWeight: '650' },
  { tag: t.emphasis, fontStyle: 'italic' },
  { tag: t.strikethrough, textDecoration: 'line-through' },
  { tag: t.link, color: 'var(--accent)' },
  { tag: t.url, color: 'var(--accent)' },
  { tag: [t.processingInstruction, t.meta, t.contentSeparator], color: 'var(--muted)' },
  { tag: t.monospace, fontFamily: 'var(--font-mono)' },
  { tag: t.quote, color: 'var(--text-2)' },
  { tag: t.keyword, color: 'var(--syn-keyword)' },
  { tag: [t.string, t.special(t.string), t.regexp], color: 'var(--syn-string)' },
  { tag: [t.number, t.bool, t.null, t.atom], color: 'var(--syn-number)' },
  { tag: [t.comment, t.lineComment, t.blockComment], color: 'var(--syn-comment)', fontStyle: 'italic' },
  { tag: [t.function(t.variableName), t.function(t.propertyName)], color: 'var(--syn-func)' },
  { tag: [t.typeName, t.className, t.namespace], color: 'var(--syn-type)' },
  { tag: [t.propertyName, t.attributeName], color: 'var(--syn-prop)' },
  { tag: [t.operator, t.punctuation], color: 'var(--text-2)' },
  { tag: t.tagName, color: 'var(--syn-keyword)' },
  { tag: t.special(t.content), color: 'var(--syn-number)' },
]);

/**
 * Enter on an empty top-level list item leaves the list *and* starts a new
 * paragraph. (The default only removes the marker, so the next line would be
 * a lazy continuation of the previous item.)
 */
function exitEmptyListItem(view: EditorView): boolean {
  const sel = view.state.selection.main;
  if (!sel.empty) return false;
  const line = view.state.doc.lineAt(sel.head);
  if (sel.head !== line.to || line.number === 1) return false;
  if (!/^([-*+]|\d+[.)])[ \t]+(\[[ xX]\][ \t]*)?$/.test(line.text)) return false;
  view.dispatch({
    changes: { from: line.from, to: line.to, insert: '\n' },
    selection: { anchor: line.from + 1 },
    scrollIntoView: true,
    userEvent: 'input',
  });
  return true;
}

export type EditorHandle = {
  view: EditorView;
  getValue(): string;
  setValue(doc: string): void;
  setMode(mode: EditorMode): void;
  setContext(ctx: LiveContext): void;
  insert(text: string): void;
  focus(): void;
  destroy(): void;
};

export function createEditor(parent: HTMLElement, opts: EditorOptions): EditorHandle {
  const modeConf = new Compartment();
  const ctxConf = new Compartment();
  let mode = opts.mode;

  const modeExt = (m: EditorMode, size: number): Extension =>
    m === 'live' && size <= LIVE_MAX_CHARS ? livePreview() : EditorView.editorAttributes.of({ class: 'cm-source' });

  const insertAt = (view: EditorView, pos: number | null, snippets: string[]) => {
    if (!snippets.length) return;
    const sel = view.state.selection.main;
    const from = pos ?? sel.from;
    const to = pos ?? sel.to;
    const before = view.state.sliceDoc(Math.max(0, from - 1), from);
    const sep = snippets.some((s) => s.startsWith('!')) ? '\n' : ' ';
    const text = (before && before !== '\n' ? sep : '') + snippets.join(sep) + sep;
    view.dispatch({ changes: { from, to, insert: text }, selection: { anchor: from + text.length }, scrollIntoView: true });
  };

  const handleFiles = (view: EditorView, files: File[], pos: number | null) => {
    if (!opts.onFiles || !files.length) return false;
    opts.onFiles(files).then((snips) => insertAt(view, pos, snips));
    return true;
  };

  const state = EditorState.create({
    doc: opts.doc,
    extensions: [
      history(),
      drawSelection(),
      indentOnInput(),
      bracketMatching(),
      closeBrackets(),
      highlightActiveLine(),
      highlightSelectionMatches(),
      EditorView.lineWrapping,
      EditorState.tabSize.of(4),
      markdown({ base: markdownLanguage, codeLanguages: languages, extensions: [MathExtension], addKeymap: false }),
      syntaxHighlighting(highlight),
      keymap.of([
        { key: 'Mod-Enter', run: () => (opts.onSubmit?.(), true) },
        { key: 'Mod-e', run: () => (opts.onToggleMode?.(), true) },
        { key: 'Enter', run: exitEmptyListItem },
        ...markdownKeymap,
        ...closeBracketsKeymap,
        ...defaultKeymap,
        ...historyKeymap,
        ...searchKeymap,
        indentWithTab,
      ]),
      opts.placeholder ? cmPlaceholder(opts.placeholder) : [],
      EditorView.contentAttributes.of({ spellcheck: 'true', autocapitalize: 'sentences', 'aria-multiline': 'true' }),
      opts.minLines ? EditorView.theme({ '.cm-content, .cm-gutter': { minHeight: `${opts.minLines * 1.6}em` } }) : [],
      ctxConf.of(liveContext.of(opts.context)),
      modeConf.of(modeExt(mode, opts.doc.length)),
      EditorView.updateListener.of((u) => {
        if (u.docChanged) {
          opts.onChange?.(u.state.doc.toString());
          // Drop out of live preview for very large documents.
          if (mode === 'live' && (u.startState.doc.length <= LIVE_MAX_CHARS) !== (u.state.doc.length <= LIVE_MAX_CHARS)) {
            u.view.dispatch({ effects: modeConf.reconfigure(modeExt(mode, u.state.doc.length)) });
          }
        }
      }),
      EditorView.domEventHandlers({
        paste(ev, view) {
          const files = Array.from(ev.clipboardData?.files ?? []);
          if (files.length && handleFiles(view, files, null)) {
            ev.preventDefault();
            return true;
          }
          return false;
        },
        drop(ev, view) {
          const files = Array.from(ev.dataTransfer?.files ?? []);
          if (!files.length) return false;
          const pos = view.posAtCoords({ x: ev.clientX, y: ev.clientY });
          if (handleFiles(view, files, pos)) {
            ev.preventDefault();
            return true;
          }
          return false;
        },
      }),
    ],
  });

  const view = new EditorView({ state, parent });
  if (opts.autofocus) view.focus();

  return {
    view,
    getValue: () => view.state.doc.toString(),
    setValue(doc: string) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: doc } });
    },
    setMode(m: EditorMode) {
      mode = m;
      view.dispatch({ effects: modeConf.reconfigure(modeExt(m, view.state.doc.length)) });
    },
    setContext(ctx: LiveContext) {
      view.dispatch({ effects: ctxConf.reconfigure(liveContext.of(ctx)) });
    },
    insert(text: string) {
      insertAt(view, null, [text]);
      view.focus();
    },
    focus: () => view.focus(),
    destroy: () => view.destroy(),
  };
}
