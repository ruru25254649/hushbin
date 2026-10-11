<script lang="ts">
  import { t } from '../lib/i18n/index.svelte';
  import { ui, allCommands, type Command } from '../lib/ui.svelte';

  let query = $state('');
  let index = $state(0);
  let input = $state<HTMLInputElement>();
  let dlg: HTMLDialogElement;
  let commands = $state<Command[]>([]);

  $effect(() => {
    if (ui.palette) {
      commands = allCommands();
      query = '';
      index = 0;
      if (!dlg.open) dlg.showModal();
      queueMicrotask(() => input?.focus());
    } else if (dlg?.open) dlg.close();
  });

  const filtered = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return commands;
    return commands.filter((c) => c.label.toLowerCase().includes(q) || c.id.includes(q));
  });

  function run(c: Command | undefined) {
    if (!c) return;
    ui.palette = false;
    queueMicrotask(c.run);
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      index = Math.min(filtered.length - 1, index + 1);
      e.preventDefault();
    } else if (e.key === 'ArrowUp') {
      index = Math.max(0, index - 1);
      e.preventDefault();
    } else if (e.key === 'Enter') {
      run(filtered[index]);
      e.preventDefault();
    }
  }
</script>

<dialog bind:this={dlg} onclose={() => (ui.palette = false)} onclick={(e) => e.target === dlg && (ui.palette = false)} aria-label={t('palette.open')}>
  {#if ui.palette}
    <input
      bind:this={input}
      bind:value={query}
      oninput={() => (index = 0)}
      onkeydown={onKey}
      placeholder={t('palette.placeholder')}
      aria-label={t('palette.placeholder')}
      role="combobox"
      aria-expanded="true"
      aria-controls="palette-list"
    />
    <ul id="palette-list" role="listbox">
      {#each filtered as c, i (c.id)}
        <li role="option" aria-selected={i === index}>
          <button class:on={i === index} onmouseenter={() => (index = i)} onclick={() => run(c)}>
            <span>{c.label}</span>
            {#if c.hint}<kbd>{c.hint}</kbd>{/if}
          </button>
        </li>
      {:else}
        <li class="empty">{t('palette.empty')}</li>
      {/each}
    </ul>
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
    width: min(560px, calc(100vw - 32px));
    margin-top: 12vh;
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 0.3);
  }
  input {
    width: 100%;
    border: none;
    border-bottom: 1px solid var(--border);
    background: none;
    outline: none;
    padding: 14px 18px;
    font-size: 16px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 6px;
    max-height: 50dvh;
    overflow: auto;
  }
  button {
    width: 100%;
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 9px 12px;
    border: none;
    border-radius: 7px;
    background: none;
    text-align: left;
    font-size: 14px;
    cursor: pointer;
  }
  button.on {
    background: var(--surface-2);
  }
  .empty {
    padding: 12px;
    color: var(--muted);
    font-size: 14px;
  }
</style>
