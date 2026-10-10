<script lang="ts" module>
  const PATHS: Record<string, string> = {
    lock: 'M5 11h14v10H5zM8 11V7a4 4 0 0 1 8 0v4',
    unlock: 'M5 11h14v10H5zM8 11V7a4 4 0 0 1 7.8-1.2',
    link: 'M10 14a4.5 4.5 0 0 0 6.4 0l3-3a4.5 4.5 0 0 0-6.4-6.4l-1 1M14 10a4.5 4.5 0 0 0-6.4 0l-3 3a4.5 4.5 0 0 0 6.4 6.4l1-1',
    copy: 'M9 9h11v11H9zM5 15H4V4h11v1',
    check: 'M4 12.5l5 5L20 6.5',
    more: 'M5 12h.01M12 12h.01M19 12h.01',
    edit: 'M4 20h4L19 9l-4-4L4 16zM13.5 6.5l4 4',
    history: 'M3 12a9 9 0 1 0 3-6.7L3 8M3 3v5h5M12 7v5l3 2',
    trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3',
    flame: 'M12 3s5 4.5 5 10a5 5 0 0 1-10 0c0-2.5 1.5-4 1.5-4S9 12 11 12c0-4 1-9 1-9z',
    clock: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2',
    sun: 'M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4',
    moon: 'M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z',
    monitor: 'M3 4h18v12H3zM8 20h8M12 16v4',
    paperclip: 'M20 11.5l-8.3 8.3a5 5 0 0 1-7.1-7.1l8.6-8.6a3.4 3.4 0 0 1 4.8 4.8l-8.6 8.6a1.7 1.7 0 0 1-2.4-2.4l7.9-7.9',
    eye: 'M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
    eyeOff: 'M3 3l18 18M10.6 5.1A10 10 0 0 1 12 5c6.5 0 10 7 10 7a17 17 0 0 1-2.6 3.4M6.6 6.6A17 17 0 0 0 2 12s3.5 7 10 7a9.7 9.7 0 0 0 5.4-1.6M9.9 9.9a3 3 0 0 0 4.2 4.2',
    x: 'M6 6l12 12M18 6L6 18',
    reply: 'M9 14L4 9l5-5M4 9h10a6 6 0 0 1 6 6v5',
    send: 'M4 12l16-8-6 16-2.5-6.5zM11.5 13.5L20 4',
    download: 'M12 4v11M7 10l5 5 5-5M4 20h16',
    globe: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.5 3.8 5.5 3.8 9s-1.3 6.5-3.8 9c-2.5-2.5-3.8-5.5-3.8-9S9.5 5.5 12 3z',
    key: 'M15 9a3 3 0 1 0 0 .01M13.5 11.5L4 21M7 18l2 2M9.5 15.5l2 2',
    keyboard: 'M3 6h18v12H3zM7 10h.01M11 10h.01M15 10h.01M7 14h10',
    command: 'M9 6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3z',
    plus: 'M12 5v14M5 12h14',
    message: 'M4 5h16v11H9l-5 4z',
    shield: 'M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z',
    code: 'M8 8l-4 4 4 4M16 8l4 4-4 4M14 4l-4 16',
    file: 'M6 3h8l4 4v14H6zM14 3v4h4',
    alert: 'M12 4l9 16H3zM12 10v4M12 17h.01',
  };
</script>

<script lang="ts">
  let { name, size = 16, class: cls = '' }: { name: string; size?: number; class?: string } = $props();
</script>

<svg class="i {cls}" width={size} height={size} viewBox="0 0 24 24" aria-hidden="true"><path d={PATHS[name] ?? ''} /></svg>
