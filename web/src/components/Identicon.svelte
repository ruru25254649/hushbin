<script lang="ts">
  // 5×5 mirrored identicon from the server-assigned author tag (hex).
  let { tag, size = 28 }: { tag: string; size?: number } = $props();

  const cells = $derived.by(() => {
    const bits: boolean[] = [];
    for (let i = 0; i < 15; i++) bits.push(((parseInt(tag[i % tag.length] || '0', 16) >> (i % 4)) & 1) === 1);
    const out: [number, number][] = [];
    for (let y = 0; y < 5; y++)
      for (let x = 0; x < 3; x++) {
        if (bits[y * 3 + x]) {
          out.push([x, y]);
          if (x < 2) out.push([4 - x, y]);
        }
      }
    return out;
  });
  const hue = $derived((parseInt(tag.slice(-4) || '0', 16) % 360) | 0);
</script>

<svg width={size} height={size} viewBox="-1 -1 7 7" aria-hidden="true" style="--h:{hue}">
  <rect x="-1" y="-1" width="7" height="7" rx="1.6" class="bg" />
  {#each cells as [x, y] (x + ',' + y)}<rect {x} {y} width="1" height="1" class="fg" />{/each}
</svg>

<style>
  svg {
    flex: none;
    border-radius: 50%;
  }
  .bg {
    fill: hsl(var(--h) 60% 92%);
  }
  .fg {
    fill: hsl(var(--h) 55% 45%);
  }
  :global(:root[data-theme='dark']) .bg {
    fill: hsl(var(--h) 30% 20%);
  }
  :global(:root[data-theme='dark']) .fg {
    fill: hsl(var(--h) 60% 65%);
  }
  @media (prefers-color-scheme: dark) {
    :global(:root:not([data-theme='light'])) .bg {
      fill: hsl(var(--h) 30% 20%);
    }
    :global(:root:not([data-theme='light'])) .fg {
      fill: hsl(var(--h) 60% 65%);
    }
  }
</style>
