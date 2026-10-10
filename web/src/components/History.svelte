<script lang="ts" module>
  export type HistoryEntry = { version: number; createdAt: number };
  export type HistorySource = {
    list: () => Promise<HistoryEntry[]>;
    text: (v: number) => Promise<string>;
    restore?: (v: number) => Promise<void>;
  };
</script>

<script lang="ts">
  import { diffLines, diffWordsWithSpace } from 'diff';
  import Dialog from './Dialog.svelte';
  import Markdown from './Markdown.svelte';
  import { t, absTime, relTime } from '../lib/i18n/index.svelte';
  import { toastError } from '../lib/ui.svelte';
  import type { AttSource } from '../lib/render/hydrate';

  let {
    open,
    onclose,
    source,
    atts = null,
  }: { open: boolean; onclose: () => void; source: HistorySource | null; atts?: AttSource | null } = $props();

  let entries = $state<HistoryEntry[]>([]);
  let selected = $state<number | null>(null);
  let view = $state<'changes' | 'preview'>('changes');
  let cur = $state('');
  let prev = $state<string | null>(null);
  let restoring = $state(false);
  const cache = new Map<number, Promise<string>>();

  const text = (v: number) => {
    let p = cache.get(v);
    if (!p) {
      p = source!.text(v);
      p.catch(() => cache.delete(v));
      cache.set(v, p);
    }
    return p;
  };

  $effect(() => {
    if (!open || !source) return;
    cache.clear();
    entries = [];
    selected = null;
    source
      .list()
      .then((l) => {
        entries = [...l].sort((a, b) => b.version - a.version);
        selected = entries[0]?.version ?? null;
      })
      .catch(toastError);
  });

  $effect(() => {
    const v = selected;
    if (v === null || !source) return;
    const idx = entries.findIndex((e) => e.version === v);
    const before = entries[idx + 1]?.version ?? null;
    Promise.all([text(v), before !== null ? text(before) : Promise.resolve(null)])
      .then(([a, b]) => {
        if (selected !== v) return;
        cur = a;
        prev = b;
      })
      .catch(toastError);
  });

  type Row = { kind: 'add' | 'del' | 'same'; parts: { text: string; mark: boolean }[] };

  const rows = $derived.by((): Row[] => {
    if (prev === null) return cur.split('\n').map((l) => ({ kind: 'add', parts: [{ text: l, mark: false }] }));
    const out: Row[] = [];
    const chunks = diffLines(prev, cur);
    for (let i = 0; i < chunks.length; i++) {
      const c = chunks[i];
      const next = chunks[i + 1];
      // A removal immediately followed by an addition: show word-level changes.
      if (c.removed && next?.added) {
        const words = diffWordsWithSpace(c.value, next.value);
        const del: Row['parts'] = [];
        const add: Row['parts'] = [];
        for (const w of words) {
          if (!w.added) del.push({ text: w.value, mark: !!w.removed });
          if (!w.removed) add.push({ text: w.value, mark: !!w.added });
        }
        out.push(...splitRows('del', del), ...splitRows('add', add));
        i++;
        continue;
      }
      const kind = c.added ? 'add' : c.removed ? 'del' : 'same';
      const lines = c.value.replace(/\n$/, '').split('\n');
      for (const l of lines) out.push({ kind, parts: [{ text: l, mark: false }] });
    }
    return out;
  });

  function splitRows(kind: Row['kind'], parts: Row['parts']): Row[] {
    const rows: Row[] = [{ kind, parts: [] }];
    for (const p of parts) {
      const segs = p.text.split('\n');
      segs.forEach((s, j) => {
        if (j > 0) rows.push({ kind, parts: [] });
        if (s) rows[rows.length - 1].parts.push({ text: s, mark: p.mark });
      });
    }
    if (rows.length > 1 && rows[rows.length - 1].parts.length === 0) rows.pop();
    return rows;
  }

  async function restore() {
    if (!source?.restore || selected === null) return;
    restoring = true;
    try {
      await source.restore(selected);
      onclose();
    } catch (e) {
      toastError(e);
    } finally {
      restoring = false;
    }
  }
</script>

<Dialog {open} {onclose} title={t('history.title')} wide>
  <div class="hist">
    <ol class="versions">
      {#each entries as e, i (e.version)}
        <li>
          <button class:on={selected === e.version} onclick={() => (selected = e.version)}>
            <span class="v">{t('history.version', { n: e.version })}{#if i === 0}<em>{t('history.current')}</em>{/if}</span>
            <span class="when" title={absTime(e.createdAt)}>{relTime(e.createdAt)}</span>
          </button>
        </li>
      {/each}
    </ol>
    <div class="detail">
      {#if selected !== null}
        <div class="bar">
          <div class="seg">
            <button class:on={view === 'changes'} onclick={() => (view = 'changes')}>
              {prev === null ? t('history.first') : t('history.changes', { from: entries[entries.findIndex((e) => e.version === selected) + 1]?.version ?? '', to: selected })}
            </button>
            <button class:on={view === 'preview'} onclick={() => (view = 'preview')}>{t('menu.rendered')}</button>
          </div>
          {#if source?.restore && selected !== entries[0]?.version}
            <button class="btn sm" disabled={restoring} onclick={restore}>{t('history.restore')}</button>
          {/if}
        </div>
        {#if view === 'changes'}
          <div class="diff" role="table">
            {#each rows as r, i (i)}
              <div class="dl {r.kind}" role="row">
                <span class="sign" aria-hidden="true">{r.kind === 'add' ? '+' : r.kind === 'del' ? '−' : ' '}</span>
                <span class="txt">{#each r.parts as p, j (j)}{#if p.mark}<mark>{p.text}</mark>{:else}{p.text}{/if}{/each}{#if !r.parts.length}&nbsp;{/if}</span>
              </div>
            {/each}
            {#if prev !== null && cur === prev}<p class="muted">{t('history.empty')}</p>{/if}
          </div>
        {:else}
          <div class="preview"><Markdown source={cur} {atts} /></div>
        {/if}
      {/if}
    </div>
  </div>
</Dialog>

<style>
  .hist {
    display: grid;
    grid-template-columns: 190px 1fr;
    gap: 16px;
    min-height: 300px;
  }
  .versions {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 2px;
    align-content: start;
    max-height: 60dvh;
    overflow: auto;
  }
  .versions button {
    width: 100%;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    border: none;
    background: none;
    padding: 7px 10px;
    border-radius: 7px;
    cursor: pointer;
    color: var(--text);
    text-align: left;
  }
  .versions button:hover {
    background: var(--surface-2);
  }
  .versions button.on {
    background: var(--accent-2);
  }
  .v {
    font-size: 14px;
    font-weight: 550;
  }
  .v em {
    font-style: normal;
    font-size: 11px;
    margin-left: 6px;
    color: var(--accent);
  }
  .when {
    font-size: 12px;
    color: var(--muted);
  }
  .detail {
    min-width: 0;
  }
  .bar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 8px;
    margin-bottom: 10px;
    flex-wrap: wrap;
  }
  .seg {
    display: inline-flex;
    padding: 3px;
    border-radius: 9px;
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  .seg button {
    border: none;
    background: none;
    border-radius: 6px;
    padding: 4px 10px;
    font-size: 13px;
    color: var(--text-2);
    cursor: pointer;
  }
  .seg button.on {
    background: var(--surface);
    color: var(--text);
  }
  .diff {
    font: 13px/1.6 var(--font-mono);
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: auto;
    max-height: 60dvh;
    color: var(--text);
  }
  .dl {
    display: flex;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .sign {
    flex: none;
    width: 22px;
    text-align: center;
    color: var(--muted);
    user-select: none;
  }
  .txt {
    flex: 1;
    padding-right: 8px;
  }
  .dl.add {
    background: color-mix(in srgb, var(--ok) 12%, transparent);
  }
  .dl.del {
    background: color-mix(in srgb, var(--danger) 12%, transparent);
  }
  .dl.add mark {
    background: color-mix(in srgb, var(--ok) 32%, transparent);
    color: inherit;
  }
  .dl.del mark {
    background: color-mix(in srgb, var(--danger) 32%, transparent);
    color: inherit;
  }
  .preview {
    color: var(--text);
    max-height: 60dvh;
    overflow: auto;
  }
  @media (max-width: 640px) {
    .hist {
      grid-template-columns: 1fr;
    }
    .versions {
      display: flex;
      overflow-x: auto;
      max-height: none;
    }
    .versions li {
      flex: none;
    }
  }
</style>
