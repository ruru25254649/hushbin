<script lang="ts">
  import type { Snippet } from 'svelte';

  // Shows a "drop to attach" overlay while files are dragged over the area.
  // Drops on the editor are handled by CodeMirror (inserted where dropped);
  // drops anywhere else in the area are passed to `ondropfiles`.
  let { children, ondropfiles, label }: { children: Snippet; ondropfiles: (files: File[]) => void; label: string } = $props();

  let el: HTMLDivElement;
  let active = $state(false);
  let depth = 0;

  const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes('Files');

  $effect(() => {
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      depth++;
      active = true;
    };
    const over = (e: DragEvent) => {
      if (hasFiles(e)) e.preventDefault();
    };
    const leave = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      depth = Math.max(0, depth - 1);
      if (depth === 0) active = false;
    };
    const drop = (e: DragEvent) => {
      depth = 0;
      active = false;
      if (e.defaultPrevented) return; // the editor took it
      const files = Array.from(e.dataTransfer?.files ?? []);
      if (!files.length) return;
      e.preventDefault();
      ondropfiles(files);
    };
    el.addEventListener('dragenter', enter);
    el.addEventListener('dragover', over);
    el.addEventListener('dragleave', leave);
    el.addEventListener('drop', drop);
    return () => {
      el.removeEventListener('dragenter', enter);
      el.removeEventListener('dragover', over);
      el.removeEventListener('dragleave', leave);
      el.removeEventListener('drop', drop);
    };
  });
</script>

<div class="dropzone" bind:this={el}>
  {@render children()}
  {#if active}
    <div class="overlay" aria-hidden="true"><span>{label}</span></div>
  {/if}
</div>

<style>
  .dropzone {
    position: relative;
    border-radius: inherit;
  }
  .overlay {
    position: absolute;
    inset: 0;
    z-index: 5;
    display: grid;
    place-items: center;
    border: 2px dashed var(--accent);
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent) 8%, transparent);
    pointer-events: none; /* let the drop reach the editor underneath */
  }
  .overlay span {
    padding: 6px 12px;
    border-radius: 99px;
    background: var(--surface);
    color: var(--accent);
    font-size: 14px;
    font-weight: 550;
    box-shadow: var(--shadow);
  }
</style>
