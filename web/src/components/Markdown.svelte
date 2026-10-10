<script lang="ts">
  import { tick } from 'svelte';
  import { renderMarkdown } from '../lib/render/markdown';
  import { hydrate, type AttSource } from '../lib/render/hydrate';
  import { t } from '../lib/i18n/index.svelte';
  import { ui } from '../lib/ui.svelte';

  let { source, atts = null }: { source: string; atts?: AttSource | null } = $props();

  let el: HTMLDivElement;
  let html = $state('');
  let seq = 0;

  const labels = () => ({
    copy: t('copy'),
    copied: t('copied'),
    download: t('download'),
    missing: t('att.missing'),
    diagramError: t('diagram.error'),
  });

  $effect(() => {
    const src = source;
    const theme = ui.themeVersion; // re-render diagrams on theme change
    const l = labels();
    const my = ++seq;
    renderMarkdown(src, l).then((h) => {
      if (my !== seq) return;
      html = h + `<!--${theme}-->`;
      tick().then(() => el && hydrate(el, atts, l));
    });
  });
</script>

<div class="markdown" bind:this={el}>{@html html}</div>
