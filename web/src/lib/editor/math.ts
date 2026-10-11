// Lezer markdown extension for `$inline$` and `$$ block $$` math.
// The rules mirror the markdown-it plugin in render/markdown.ts so the
// editor and the reader agree on what counts as math.

import type { BlockContext, Line, MarkdownConfig, InlineContext, Element } from '@lezer/markdown';
import { tags } from '@lezer/highlight';

const DOLLAR = 36;
const BACKSLASH = 92;

function isSpace(c: number) {
  return c === 32 || c === 9 || c === 10 || c === 13;
}

function isDigit(c: number) {
  return c >= 48 && c <= 57;
}

/** Index of the closing `$` for an inline math span opening at `pos`, or -1. */
export function findInlineMathEnd(char: (i: number) => number, pos: number, end: number): number {
  const start = pos + 1;
  if (start >= end || isSpace(char(start)) || char(start) === DOLLAR) return -1;
  for (let i = start; i < end; i++) {
    const c = char(i);
    if (c === BACKSLASH) {
      i++;
      continue;
    }
    if (c === DOLLAR) {
      if (isSpace(char(i - 1))) continue;
      if (i + 1 < end && isDigit(char(i + 1))) continue;
      return i;
    }
  }
  return -1;
}

export const MathExtension: MarkdownConfig = {
  defineNodes: [
    { name: 'InlineMath', style: tags.special(tags.content) },
    { name: 'InlineMathMark', style: tags.processingInstruction },
    { name: 'BlockMath', block: true, style: tags.special(tags.content) },
    { name: 'BlockMathMark', style: tags.processingInstruction },
  ],
  parseInline: [
    {
      name: 'InlineMath',
      before: 'Emphasis',
      parse(cx: InlineContext, next: number, pos: number) {
        if (next !== DOLLAR) return -1;
        // `$$` inside a paragraph is not math; skip both characters.
        if (cx.char(pos + 1) === DOLLAR) return -1;
        if (pos > cx.offset && (cx.char(pos - 1) === BACKSLASH || cx.char(pos - 1) === DOLLAR)) return -1;
        const close = findInlineMathEnd((i) => cx.char(i), pos, cx.end);
        if (close < 0) return -1;
        return cx.addElement(
          cx.elt('InlineMath', pos, close + 1, [cx.elt('InlineMathMark', pos, pos + 1), cx.elt('InlineMathMark', close, close + 1)]),
        );
      },
    },
  ],
  parseBlock: [
    {
      name: 'BlockMath',
      before: 'FencedCode',
      parse(cx: BlockContext, line: Line) {
        if (line.indent - line.baseIndent >= 4) return false;
        if (line.next !== DOLLAR || line.text.charCodeAt(line.pos + 1) !== DOLLAR) return false;
        const from = cx.lineStart + line.pos;
        const marks: Element[] = [cx.elt('BlockMathMark', from, from + 2)];
        const rest = line.text.slice(line.pos + 2).trimEnd();
        // Single line: $$ x^2 $$
        if (rest.length >= 2 && rest.endsWith('$$')) {
          const closeAt = cx.lineStart + line.pos + 2 + rest.length - 2;
          marks.push(cx.elt('BlockMathMark', closeAt, closeAt + 2));
          cx.addElement(cx.elt('BlockMath', from, closeAt + 2, marks));
          cx.nextLine();
          return true;
        }
        const depth = () => (line as any).depth as number;
        const stack = () => ((cx as any).stack as unknown[]).length;
        while (cx.nextLine() && depth() >= stack()) {
          for (const m of line.markers) marks.push(m);
          const t = line.text.trimEnd();
          if (t.endsWith('$$') && t.length - 2 >= line.pos) {
            const closeAt = cx.lineStart + t.length - 2;
            marks.push(cx.elt('BlockMathMark', closeAt, closeAt + 2));
            cx.addElement(cx.elt('BlockMath', from, closeAt + 2, marks));
            cx.nextLine();
            return true;
          }
        }
        cx.addElement(cx.elt('BlockMath', from, cx.prevLineEnd(), marks));
        return true;
      },
      endLeaf(_cx: BlockContext, line: Line) {
        return line.indent - line.baseIndent < 4 && line.next === DOLLAR && line.text.charCodeAt(line.pos + 1) === DOLLAR;
      },
    },
  ],
};
