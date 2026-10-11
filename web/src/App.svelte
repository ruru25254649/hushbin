<script lang="ts">
  import NewPaste from './routes/NewPaste.svelte';
  import ViewPaste from './routes/ViewPaste.svelte';
  import Dialog from './components/Dialog.svelte';
  import Icon from './components/Icon.svelte';
  import CommandPalette from './components/CommandPalette.svelte';
  import { t, i18n, LANGS, setLang, type Lang } from './lib/i18n/index.svelte';
  import {
    route,
    navigate,
    prefs,
    updatePrefs,
    cycleTheme,
    toasts,
    ui,
    provideCommands,
    modKey,
  } from './lib/ui.svelte';

  const pasteId = $derived(route.path.match(/^\/p\/([A-Za-z0-9]{4,32})\/?$/)?.[1] ?? null);

  provideCommands('app', () => [
    { id: 'new', label: t('nav.new'), run: () => navigate('/') },
    { id: 'theme', label: `${t('theme.label')}: ${t(`theme.${prefs.theme}`)}`, run: cycleTheme },
    ...LANGS.map((l) => ({ id: 'lang-' + l.code, label: `${t('lang.label')}: ${l.label}`, run: () => changeLang(l.code) })),
    { id: 'shortcuts', label: t('shortcuts.title'), hint: '?', run: () => (ui.shortcuts = true) },
  ]);

  function changeLang(l: Lang) {
    updatePrefs({ lang: l });
    setLang(l);
  }

  function isTyping(e: KeyboardEvent) {
    const el = e.target as HTMLElement;
    return el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName);
  }

  function onKey(e: KeyboardEvent) {
    const mod = e.ctrlKey || e.metaKey;
    if (mod && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      ui.palette = !ui.palette;
    } else if (e.key === '?' && !mod && !isTyping(e)) {
      e.preventDefault();
      ui.shortcuts = true;
    }
  }

  const themeIcon = $derived(prefs.theme === 'light' ? 'sun' : prefs.theme === 'dark' ? 'moon' : 'monitor');

  function newPaste(e: MouseEvent) {
    e.preventDefault();
    if (route.path === '/') return;
    navigate('/');
  }
</script>

<svelte:window onkeydown={onKey} />

<header class="top">
  <a href="/" class="brand" onclick={newPaste}>
    <span class="logo" aria-hidden="true"><Icon name="shield" size={18} /></span>
    <span class="name">Hushbin</span>
  </a>
  <span class="tag muted">{t('app.tagline')}</span>
  <nav>
    {#if pasteId}
      <a href="/" class="btn sm" onclick={newPaste}><Icon name="plus" /><span class="lbl">{t('nav.new')}</span></a>
    {/if}
    <button class="btn ghost sm icon" title="{t('palette.open')} ({modKey}+K)" aria-label={t('palette.open')} onclick={() => (ui.palette = true)}><Icon name="command" /></button>
    <label class="lang" title={t('lang.label')}>
      <Icon name="globe" />
      <select value={i18n.lang} onchange={(e) => changeLang(e.currentTarget.value as Lang)} aria-label={t('lang.label')}>
        {#each LANGS as l (l.code)}<option value={l.code}>{l.label}</option>{/each}
      </select>
    </label>
    <button class="btn ghost sm icon" title="{t('theme.label')}: {t(`theme.${prefs.theme}`)}" aria-label="{t('theme.label')}: {t(`theme.${prefs.theme}`)}" onclick={cycleTheme}><Icon name={themeIcon} /></button>
  </nav>
</header>

<main>
  {#if pasteId}
    {#key pasteId}<ViewPaste id={pasteId} />{/key}
  {:else}
    <NewPaste />
  {/if}
</main>

<div class="toasts" role="status" aria-live="polite">
  {#each toasts as tst (tst.id)}
    <div class="toast {tst.kind}">{tst.text}</div>
  {/each}
</div>

<Dialog
  open={!!ui.confirm}
  onclose={() => {
    ui.confirm?.resolve(false);
    ui.confirm = null;
  }}
  title={ui.confirm?.title ?? ''}
>
  <p class="confirm-body">{ui.confirm?.body}</p>
  {#snippet footer()}
    <button
      class="btn"
      onclick={() => {
        ui.confirm?.resolve(false);
        ui.confirm = null;
      }}>{t('cancel')}</button
    >
    <button
      class="btn {ui.confirm?.danger ? 'danger' : 'primary'}"
      onclick={() => {
        ui.confirm?.resolve(true);
        ui.confirm = null;
      }}>{ui.confirm?.confirm}</button
    >
  {/snippet}
</Dialog>

<Dialog open={ui.shortcuts} onclose={() => (ui.shortcuts = false)} title={t('shortcuts.title')}>
  <dl class="keys">
    <dt><kbd>{modKey}</kbd> <kbd>Enter</kbd></dt><dd>{t('shortcuts.submit')}</dd>
    <dt><kbd>{modKey}</kbd> <kbd>E</kbd></dt><dd>{t('shortcuts.mode')}</dd>
    <dt><kbd>{modKey}</kbd> <kbd>K</kbd></dt><dd>{t('shortcuts.palette')}</dd>
    <dt><kbd>{modKey}</kbd> <kbd>Shift</kbd> <kbd>C</kbd></dt><dd>{t('shortcuts.copyLink')}</dd>
    <dt><kbd>?</kbd></dt><dd>{t('shortcuts.help')}</dd>
    <dt><kbd>Esc</kbd></dt><dd>{t('shortcuts.close')}</dd>
  </dl>
</Dialog>

<CommandPalette />

<style>
  .top {
    display: flex;
    align-items: center;
    gap: 14px;
    max-width: 900px;
    margin: 0 auto;
    padding: max(10px, env(safe-area-inset-top)) 16px 10px;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text);
    text-decoration: none;
    font-weight: 680;
    font-size: 16px;
    letter-spacing: -0.01em;
  }
  .logo {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border-radius: 8px;
    background: var(--text);
    color: var(--bg);
  }
  .tag {
    font-size: 13px;
  }
  nav {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 4px;
  }
  nav a.btn {
    text-decoration: none;
  }
  .lang {
    position: relative;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-sm);
    color: var(--text);
    cursor: pointer;
  }
  .lang:hover {
    background: var(--surface-2);
  }
  .lang select {
    position: absolute;
    inset: 0;
    opacity: 0;
    cursor: pointer;
  }
  main {
    max-width: 900px;
    margin: 0 auto;
    padding: 6px 16px max(48px, env(safe-area-inset-bottom));
  }
  .toasts {
    position: fixed;
    left: 50%;
    bottom: max(20px, env(safe-area-inset-bottom));
    transform: translateX(-50%);
    display: grid;
    gap: 8px;
    z-index: 100;
    width: max-content;
    max-width: calc(100vw - 32px);
    pointer-events: none;
  }
  .toast {
    padding: 9px 16px;
    border-radius: 10px;
    background: var(--text);
    color: var(--bg);
    font-size: 14px;
    box-shadow: var(--shadow-lg);
    animation: rise 0.18s ease-out;
    text-align: center;
  }
  .toast.error {
    background: var(--danger);
    color: #fff;
  }
  @keyframes rise {
    from {
      transform: translateY(8px);
      opacity: 0;
    }
  }
  .confirm-body {
    margin: 0;
    line-height: 1.55;
  }
  .keys {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 10px 16px;
    margin: 0;
    align-items: center;
  }
  .keys dt {
    white-space: nowrap;
  }
  .keys dd {
    margin: 0;
    color: var(--text);
  }
  @media (max-width: 640px) {
    .tag,
    nav .lbl {
      display: none;
    }
  }
</style>
