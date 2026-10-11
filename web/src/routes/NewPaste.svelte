<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import Editor from '../components/Editor.svelte';
  import Icon from '../components/Icon.svelte';
  import PasswordField from '../components/PasswordField.svelte';
  import { t, renderLabels } from '../lib/i18n/index.svelte';
  import {
    prefs,
    updatePrefs,
    server,
    navigate,
    toast,
    toastError,
    confirmDialog,
    copy,
    provideCommands,
    modKey,
  } from '../lib/ui.svelte';
  import { createPaste, shareLink, attachmentMarkdown } from '../lib/paste';
  import { LocalFiles, stageFiles } from '../lib/local';
  import { setHandoff } from '../lib/handoff';
  import { draft } from '../lib/storage';
  import { toB64u } from '../lib/b64';
  import { api } from '../lib/api';
  import { takeShared } from '../lib/share';
  import type { EditorHandle } from '../lib/editor/editor';
  import type { LiveContext } from '../lib/editor/livePreview';

  let title = $state('');
  let body = $state(draft.load() ?? '');
  let usePassword = $state(true);
  let password = $state('');
  let pwInvalid = $state(false);
  let busy = $state(false);
  let editor = $state<EditorHandle>();
  let fileInput = $state<HTMLInputElement>()!;
  let pwBox = $state<HTMLDivElement>();
  let createdOnce = $state<{ id: string; link: string; owner: string } | null>(null);

  const files = new LocalFiles();

  const expiries = $derived(server.config.expiries);
  const isOnce = $derived(prefs.mode === 'once');
  const canCreate = $derived(!busy && (body.trim().length > 0 || title.trim().length > 0));

  $effect(() => {
    if (!expiries.includes(prefs.expiresIn)) updatePrefs({ expiresIn: expiries.includes(604800) ? 604800 : expiries[0] });
  });

  $effect(() => {
    draft.save(body);
  });

  const context: LiveContext = {
    atts: files,
    get labels() {
      return renderLabels();
    },
  };

  async function addFiles(input: File[]): Promise<string[]> {
    const already = [...files.files.values()].reduce((n, f) => n + f.data.length, 0);
    const { files: staged, errors } = await stageFiles(input, server.config.maxAttachment, server.config.maxPasteAttachments, already);
    for (const e of errors) toast(e, 'error', 4000);
    for (const f of staged) files.add(f);
    return staged.map(attachmentMarkdown);
  }

  async function pickFiles() {
    const list = Array.from(fileInput.files ?? []);
    fileInput.value = '';
    if (!list.length) return;
    const snippets = await addFiles(list);
    if (snippets.length) editor?.insert(snippets.join('\n'));
  }

  async function turnOffPassword() {
    const ok = await confirmDialog({
      title: t('password.warn.title'),
      body: t('password.warn.body'),
      confirm: t('password.warn.confirm'),
      danger: true,
    });
    if (ok) {
      usePassword = false;
      password = '';
      pwInvalid = false;
    }
  }

  async function create() {
    if (!canCreate) return;
    const needPw = !isOnce && usePassword;
    if (needPw && !password) {
      pwInvalid = true;
      toast(t('password.required'), 'error');
      pwBox?.querySelector('input')?.focus();
      return;
    }
    busy = true;
    try {
      const pw = isOnce ? password || null : usePassword ? password : null;
      const res = await createPaste({
        burn: isOnce,
        discussion: !isOnce && prefs.discussion,
        expiresIn: prefs.expiresIn,
        password: pw,
        rememberPassword: !isOnce && prefs.rememberPw,
        title: title.trim(),
        body,
        files: files.referenced(body),
      });
      draft.save(null);
      const key = res.unlocked.key;
      if (isOnce) {
        createdOnce = { id: res.id, link: shareLink(res.id, key), owner: res.owner };
        copy(createdOnce.link, t('copied'));
      } else {
        setHandoff({
          id: res.id,
          unlocked: res.unlocked,
          plain: { v: 1, title: title.trim(), body },
          files: files.referenced(body),
          expiresAt: res.expiresAt,
          discussion: prefs.discussion,
          hasPassword: !!pw,
        });
        navigate(`/p/${res.id}#${toB64u(key)}`);
      }
    } catch (e) {
      toastError(e);
    } finally {
      busy = false;
    }
  }

  async function destroyOnce() {
    if (!createdOnce) return;
    const ok = await confirmDialog({ title: t('destroy.title'), body: t('destroy.body'), confirm: t('destroy.confirm'), danger: true });
    if (!ok) return;
    try {
      await api.destroy(createdOnce.id, createdOnce.owner);
      toast(t('destroyed'));
      reset();
    } catch (e) {
      toastError(e);
    }
  }

  function reset() {
    createdOnce = null;
    title = '';
    body = '';
    password = '';
    files.clear();
  }

  const toggleMode = () => updatePrefs({ editor: prefs.editor === 'live' ? 'source' : 'live' });

  onMount(async () => {
    if (new URLSearchParams(location.search).has('share')) {
      history.replaceState(null, '', '/');
      const shared = await takeShared();
      if (shared) {
        if (shared.title && !title) title = shared.title;
        const snippets = shared.files.length ? await addFiles(shared.files) : [];
        const add = [shared.text, ...snippets].filter(Boolean).join('\n\n');
        if (add) body = body ? body + '\n\n' + add : add;
        toast(t('share.received'));
      }
    }
  });

  const unregister = provideCommands('new', () => [
    { id: 'create', label: t('create'), hint: `${modKey}+Enter`, run: create },
    { id: 'mode', label: t('mode.toggle'), hint: `${modKey}+E`, run: toggleMode },
    { id: 'attach', label: t('attach'), run: () => fileInput.click() },
    { id: 'once', label: `${t('type.label')}: ${t('type.once')}`, run: () => updatePrefs({ mode: 'once' }) },
    { id: 'keep', label: `${t('type.label')}: ${t('type.keep')}`, run: () => updatePrefs({ mode: 'keep' }) },
  ]);
  onDestroy(() => {
    unregister();
    files.clear();
  });
</script>

<svelte:window
  onkeydown={(e) => {
    // Ctrl/⌘+Enter from anywhere on the form (title, options); the editor handles its own.
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter' && !e.defaultPrevented && !createdOnce) {
      e.preventDefault();
      create();
    }
  }}
/>

{#if createdOnce}
  <section class="created card">
    <div class="created-icon"><Icon name="flame" size={22} /></div>
    <h1>{t('created.title')}</h1>
    <p class="muted">{t('created.once')}</p>
    <div class="linkrow">
      <input class="input mono" readonly value={createdOnce.link} onfocus={(e) => e.currentTarget.select()} aria-label={t('copy.link')} />
      <button class="btn primary" onclick={() => copy(createdOnce!.link)}><Icon name="copy" />{t('copy')}</button>
    </div>
    {#if password}<p class="hint"><Icon name="key" /> {t('password.sendSeparately')}</p>{/if}
    <div class="actions">
      <button class="btn" onclick={reset}><Icon name="plus" />{t('created.another')}</button>
      <button class="btn ghost danger-text" onclick={destroyOnce}><Icon name="trash" />{t('menu.destroy')}</button>
    </div>
  </section>
{:else}
  <section class="sheet card">
    <div class="sheet-head">
      <input class="title" bind:value={title} placeholder={t('new.title')} aria-label={t('new.title')} maxlength="200" />
      <div class="tools">
        <div class="seg small" role="group" aria-label={t('mode.toggle')}>
          <button class:on={prefs.editor === 'live'} aria-pressed={prefs.editor === 'live'} onclick={() => updatePrefs({ editor: 'live' })}>{t('mode.live')}</button>
          <button class:on={prefs.editor === 'source'} aria-pressed={prefs.editor === 'source'} onclick={() => updatePrefs({ editor: 'source' })}>{t('mode.source')}</button>
        </div>
        <button class="btn ghost sm icon" title={t('attach')} aria-label={t('attach')} onclick={() => fileInput.click()}><Icon name="paperclip" /></button>
        <input bind:this={fileInput} type="file" multiple hidden onchange={pickFiles} />
      </div>
    </div>
    <div class="sheet-body">
      <Editor
        bind:value={body}
        bind:handle={editor}
        mode={prefs.editor}
        {context}
        placeholder={t('new.body')}
        label={t('new.body')}
        autofocus
        minLines={12}
        onsubmit={create}
        ontogglemode={toggleMode}
        onfiles={addFiles}
      />
    </div>
  </section>

  <section class="opts card">
    <div class="seg type" role="radiogroup" aria-label={t('type.label')}>
      <button role="radio" aria-checked={!isOnce} class:on={!isOnce} onclick={() => updatePrefs({ mode: 'keep' })}>
        <Icon name="lock" /><span><b>{t('type.keep')}</b><small>{t('type.keep.hint')}</small></span>
      </button>
      <button role="radio" aria-checked={isOnce} class:on={isOnce} onclick={() => updatePrefs({ mode: 'once' })}>
        <Icon name="flame" /><span><b>{t('type.once')}</b><small>{t('type.once.hint')}</small></span>
      </button>
    </div>

    <div class="row">
      <label class="field">
        <span>{t('expiry.label')}</span>
        <select class="input" value={prefs.expiresIn} onchange={(e) => updatePrefs({ expiresIn: Number(e.currentTarget.value) })}>
          {#each expiries as s (s)}<option value={s}>{t(`expiry.${s}` as any)}</option>{/each}
        </select>
      </label>
      {#if !isOnce}
        <label class="check discussion">
          <input type="checkbox" checked={prefs.discussion} onchange={(e) => updatePrefs({ discussion: e.currentTarget.checked })} />
          <Icon name="message" />{t('discussion.toggle')}
        </label>
      {/if}
    </div>

    <div class="pwsec" bind:this={pwBox}>
      {#if isOnce}
        <PasswordField bind:value={password} placeholder={t('password.optional')} />
      {:else if usePassword}
        <PasswordField bind:value={password} placeholder={t('password.label')} invalid={pwInvalid} onenter={create} />
        <div class="pwrow">
          <label class="check"><input type="checkbox" checked={prefs.rememberPw} onchange={(e) => updatePrefs({ rememberPw: e.currentTarget.checked })} />{t('remember')}</label>
          <button class="linkbtn" onclick={turnOffPassword}>{t('password.off')}</button>
        </div>
      {:else}
        <div class="notice warn nopw">
          <Icon name="alert" /><span>{t('password.none')}</span>
          <button class="linkbtn" onclick={() => (usePassword = true)}>{t('password.useOne')}</button>
        </div>
      {/if}
    </div>

    <div class="submit">
      <span class="muted kbdhint"><kbd>{modKey}</kbd> <kbd>Enter</kbd></span>
      <button class="btn primary create" disabled={!canCreate} onclick={create}>
        <Icon name={isOnce ? 'flame' : 'lock'} />{busy ? t('creating') : t('create')}
      </button>
    </div>
  </section>
{/if}

<style>
  .sheet {
    overflow: hidden;
  }
  .sheet-head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px 6px 18px;
    border-bottom: 1px solid var(--border);
  }
  .title {
    flex: 1;
    min-width: 0;
    border: none;
    outline: none;
    background: none;
    font-size: 18px;
    font-weight: 620;
    padding: 6px 0;
  }
  .title::placeholder {
    color: var(--muted);
    font-weight: 500;
  }
  .tools {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .sheet-body {
    padding: 10px 18px 6px;
    min-height: 280px;
  }

  .seg {
    display: inline-flex;
    padding: 3px;
    border-radius: 9px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    gap: 2px;
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
    box-shadow: 0 1px 2px rgb(0 0 0 / 0.08);
  }

  .opts {
    margin-top: 14px;
    padding: 14px;
    display: grid;
    gap: 14px;
  }
  .seg.type {
    display: grid;
    grid-template-columns: 1fr 1fr;
    width: 100%;
  }
  .seg.type button {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    text-align: left;
  }
  .seg.type span {
    display: flex;
    flex-direction: column;
    line-height: 1.3;
  }
  .seg.type b {
    font-weight: 600;
    font-size: 14px;
  }
  .seg.type small {
    font-size: 12px;
    color: var(--muted);
  }
  .seg.type button.on :global(.i) {
    color: var(--accent);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 16px;
    flex-wrap: wrap;
  }
  .field {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 14px;
    color: var(--text-2);
  }
  .field select {
    width: auto;
    min-width: 130px;
    height: 34px;
  }
  .discussion :global(.i) {
    color: var(--muted);
  }
  .pwsec {
    display: grid;
    gap: 8px;
  }
  .pwrow {
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
  .linkbtn {
    border: none;
    background: none;
    color: var(--text-2);
    font-size: 13px;
    text-decoration: underline;
    text-underline-offset: 3px;
    cursor: pointer;
    padding: 2px;
  }
  .nopw {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .nopw .linkbtn {
    margin-left: auto;
    color: inherit;
  }
  .submit {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 12px;
  }
  .create {
    min-width: 120px;
    height: 38px;
  }
  .kbdhint {
    font-size: 12px;
  }

  .created {
    padding: 28px 24px;
    text-align: center;
    display: grid;
    gap: 10px;
    justify-items: center;
  }
  .created h1 {
    margin: 0;
    font-size: 20px;
  }
  .created p {
    margin: 0;
  }
  .created-icon {
    width: 44px;
    height: 44px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    background: var(--accent-2);
    color: var(--accent);
  }
  .linkrow {
    display: flex;
    gap: 8px;
    width: 100%;
    max-width: 560px;
    margin-top: 6px;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 13px;
  }
  .hint {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 13px;
    color: var(--text-2);
  }
  .actions {
    display: flex;
    gap: 8px;
    margin-top: 8px;
  }
  .danger-text {
    color: var(--danger);
  }

  @media (max-width: 560px) {
    .sheet-body {
      padding: 8px 12px 4px;
    }
    .sheet-head {
      padding-left: 12px;
    }
    .seg.type button {
      padding: 8px;
    }
    .kbdhint {
      display: none;
    }
    .create {
      width: 100%;
    }
  }
</style>
