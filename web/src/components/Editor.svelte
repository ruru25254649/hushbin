<script lang="ts">
  import { onMount } from 'svelte';
  import type { EditorHandle, EditorMode } from '../lib/editor/editor';
  import type { LiveContext } from '../lib/editor/livePreview';

  let {
    value = $bindable(''),
    mode,
    context,
    placeholder = '',
    autofocus = false,
    minLines = 0,
    label = '',
    onsubmit,
    ontogglemode,
    onfiles,
    handle = $bindable(),
  }: {
    value?: string;
    mode: EditorMode;
    context: LiveContext;
    placeholder?: string;
    autofocus?: boolean;
    minLines?: number;
    label?: string;
    onsubmit?: () => void;
    ontogglemode?: () => void;
    onfiles?: (files: File[]) => Promise<string[]>;
    handle?: EditorHandle;
  } = $props();

  let host: HTMLDivElement;
  let ed: EditorHandle | null = null;
  let ready = $state(false);

  onMount(() => {
    let destroyed = false;
    // CodeMirror is loaded on demand so read-only visitors never download it.
    import('../lib/editor/editor').then(({ createEditor }) => {
      if (destroyed) return;
      mountEditor(createEditor);
    });
    return () => {
      destroyed = true;
      ed?.destroy();
    };
  });

  function mountEditor(createEditor: typeof import('../lib/editor/editor').createEditor) {
    ed = createEditor(host, {
      doc: value,
      mode,
      context,
      placeholder,
      autofocus,
      minLines,
      onChange: (d) => (value = d),
      onSubmit: () => onsubmit?.(),
      onToggleMode: () => ontogglemode?.(),
      onFiles: onfiles,
    });
    if (label) ed.view.contentDOM.setAttribute('aria-label', label);
    handle = ed;
    ready = true;
  }

  $effect(() => {
    if (ready) ed?.setMode(mode);
  });

  $effect(() => {
    if (ready) ed?.setContext(context);
  });

  // External value changes (e.g. reset after send).
  $effect(() => {
    if (ready && ed && value !== ed.getValue()) ed.setValue(value);
  });
</script>

<div class="editor" class:loading={!ready} bind:this={host}></div>

<style>
  .editor {
    min-width: 0;
  }
  .editor.loading {
    min-height: 3em;
  }
</style>
