<script lang="ts" module>
  export type MenuItem =
    | { label: string; icon?: string; danger?: boolean; hint?: string; run: () => void }
    | { separator: true };
</script>

<script lang="ts">
  import Icon from './Icon.svelte';

  let { items, label, icon = 'more' }: { items: MenuItem[]; label: string; icon?: string } = $props();

  let open = $state(false);
  let root: HTMLDivElement;
  let list = $state<HTMLDivElement>();

  function toggle() {
    open = !open;
    if (open) queueMicrotask(() => list?.querySelector<HTMLButtonElement>('button')?.focus());
  }

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    const btns = Array.from(list?.querySelectorAll<HTMLButtonElement>('button') ?? []);
    const i = btns.indexOf(document.activeElement as HTMLButtonElement);
    if (e.key === 'Escape') {
      open = false;
      root.querySelector<HTMLButtonElement>('.trigger')?.focus();
      e.preventDefault();
    } else if (e.key === 'ArrowDown') {
      btns[(i + 1) % btns.length]?.focus();
      e.preventDefault();
    } else if (e.key === 'ArrowUp') {
      btns[(i - 1 + btns.length) % btns.length]?.focus();
      e.preventDefault();
    }
  }

  function onDocClick(e: MouseEvent) {
    if (open && !root.contains(e.target as Node)) open = false;
  }
</script>

<svelte:document onclick={onDocClick} />

<div class="menu" bind:this={root} onkeydown={onKey} role="presentation">
  <button class="btn ghost icon trigger" aria-haspopup="menu" aria-expanded={open} aria-label={label} title={label} onclick={toggle}>
    <Icon name={icon} />
  </button>
  {#if open}
    <div class="list card" role="menu" bind:this={list}>
      {#each items as item, i (i)}
        {#if 'separator' in item}
          <div class="sep" role="separator"></div>
        {:else}
          <button
            role="menuitem"
            class:danger={item.danger}
            onclick={() => {
              open = false;
              item.run();
            }}
          >
            {#if item.icon}<Icon name={item.icon} />{/if}
            <span>{item.label}</span>
            {#if item.hint}<span class="hint">{item.hint}</span>{/if}
          </button>
        {/if}
      {/each}
    </div>
  {/if}
</div>

<style>
  .menu {
    position: relative;
  }
  .list {
    position: absolute;
    right: 0;
    top: calc(100% + 6px);
    z-index: 30;
    min-width: 220px;
    padding: 5px;
    box-shadow: var(--shadow-lg);
  }
  button[role='menuitem'] {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 7px 10px;
    border: none;
    border-radius: 6px;
    background: none;
    text-align: left;
    font-size: 14px;
    cursor: pointer;
    color: var(--text);
  }
  button[role='menuitem']:hover,
  button[role='menuitem']:focus-visible {
    background: var(--surface-2);
    outline: none;
  }
  button.danger {
    color: var(--danger);
  }
  .hint {
    margin-left: auto;
    font-size: 12px;
    color: var(--muted);
  }
  .sep {
    height: 1px;
    margin: 4px 6px;
    background: var(--border);
  }
</style>
