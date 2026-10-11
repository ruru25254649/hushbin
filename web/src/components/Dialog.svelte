<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  import { t } from '../lib/i18n/index.svelte';

  let {
    open,
    onclose,
    title = '',
    wide = false,
    children,
    footer,
  }: {
    open: boolean;
    onclose: () => void;
    title?: string;
    wide?: boolean;
    children: Snippet;
    footer?: Snippet;
  } = $props();

  let dlg: HTMLDialogElement;

  $effect(() => {
    if (!dlg) return;
    if (open && !dlg.open) dlg.showModal();
    if (!open && dlg.open) dlg.close();
  });
</script>

<dialog
  bind:this={dlg}
  class:wide
  onclose={() => open && onclose()}
  onclick={(e) => e.target === dlg && onclose()}
>
  {#if open}
    <div class="inner">
      {#if title}
        <header>
          <h2>{title}</h2>
          <button class="btn ghost sm icon" onclick={onclose} aria-label={t('close')}><Icon name="x" /></button>
        </header>
      {/if}
      <div class="body">{@render children()}</div>
      {#if footer}<footer>{@render footer()}</footer>{/if}
    </div>
  {/if}
</dialog>

<style>
  dialog {
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 14px;
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-lg);
    width: min(440px, calc(100vw - 32px));
    max-height: min(86dvh, 900px);
  }
  dialog.wide {
    width: min(860px, calc(100vw - 32px));
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 0.35);
    backdrop-filter: blur(2px);
  }
  .inner {
    display: flex;
    flex-direction: column;
    max-height: inherit;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 14px 4px 20px;
  }
  h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 620;
  }
  .body {
    padding: 8px 20px 18px;
    overflow: auto;
    font-size: 14.5px;
    color: var(--text-2);
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 12px 16px;
    border-top: 1px solid var(--border);
  }
</style>
