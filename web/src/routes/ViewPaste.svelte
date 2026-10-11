<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import Markdown from '../components/Markdown.svelte';
  import Editor from '../components/Editor.svelte';
  import Icon from '../components/Icon.svelte';
  import Menu, { type MenuItem } from '../components/Menu.svelte';
  import PasswordField from '../components/PasswordField.svelte';
  import History, { type HistorySource } from '../components/History.svelte';
  import Discussion from '../components/Discussion.svelte';
  import { t, relTime, absTime, renderLabels } from '../lib/i18n/index.svelte';
  import {
    prefs,
    updatePrefs,
    navigate,
    toast,
    toastError,
    confirmDialog,
    copy,
    provideCommands,
    modKey,
    server,
  } from '../lib/ui.svelte';
  import { api, subscribe, ApiError, type PasteHeader, type LiveEvent } from '../lib/api';
  import {
    parseFragment,
    shareLink,
    ownerLink,
    unlock,
    openPaste,
    rememberedP,
    decryptBody,
    updatePaste,
    uploadFile,
    attachmentMarkdown,
    type Unlocked,
  } from '../lib/paste';
  import { DecryptError, type BodyPlain } from '../lib/crypto';
  import { secrets } from '../lib/storage';
  import { toB64u } from '../lib/b64';
  import { takeHandoff } from '../lib/handoff';
  import { PasteStore } from '../lib/pasteStore.svelte';
  import { LocalFiles, stageFiles } from '../lib/local';
  import type { LiveContext } from '../lib/editor/livePreview';
  import type { EditorHandle } from '../lib/editor/editor';

  let { id }: { id: string } = $props();

  type Phase = 'loading' | 'badlink' | 'notfound' | 'burnconfirm' | 'password' | 'decrypting' | 'ready' | 'destroyed' | 'error';
  let phase = $state<Phase>('loading');
  let errorMsg = $state('');
  let header = $state<PasteHeader | null>(null);
  let key = $state<Uint8Array<ArrayBuffer> | null>(null);
  let store = $state<PasteStore | null>(null);
  let owner = $state<string | null>(null);

  let password = $state('');
  let pwWrong = $state(false);
  let remember = $state(prefs.rememberPw);
  let burned = $state(false);
  let justCreated = $state(false);
  let createdHasPassword = $state(false);
  let showSource = $state(false);
  let historyOpen = $state(false);

  // editing
  let editing = $state(false);
  let editTitle = $state('');
  let editBody = $state('');
  let saving = $state(false);
  let editBase = 0;
  let editor = $state<EditorHandle>();
  let editFiles = $state<LocalFiles | null>(null);
  const editUploaded = new Set<string>();
  let editFileInput = $state<HTMLInputElement>();

  let unsubscribe: (() => void) | null = null;

  const labels = renderLabels;

  onMount(start);
  onDestroy(() => {
    unsubscribe?.();
    store?.dispose();
    unregister();
  });

  async function start() {
    const frag = parseFragment(location.hash);
    if (!frag) {
      phase = 'badlink';
      return;
    }
    key = frag.key;
    if (frag.owner) {
      secrets.setOwner(id, frag.owner);
      history.replaceState(null, '', `/p/${id}#${toB64u(frag.key)}`);
    }
    owner = secrets.owner(id);

    const h = takeHandoff(id);
    if (h) {
      const s = new PasteStore(h.unlocked, owner);
      for (const f of h.files) s.atts.add(f);
      s.plain = h.plain;
      s.version = 1;
      s.createdAt = s.updatedAt = Date.now();
      s.expiresAt = h.expiresAt;
      s.discussion = h.discussion;
      wireImages(s);
      store = s;
      justCreated = true;
      createdHasPassword = h.hasPassword;
      phase = 'ready';
      // Pick up server-side attachment records for later edits.
      openPaste(h.unlocked)
        .then((r) => s.addAttachments(r.paste.attachments))
        .catch(() => {});
      live(s);
      return;
    }

    try {
      header = await api.header(id);
    } catch (e) {
      phase = e instanceof ApiError && e.status === 404 ? 'notfound' : 'error';
      errorMsg = e instanceof ApiError ? e.message : String(e);
      return;
    }

    if (header.burn) {
      phase = 'burnconfirm';
      return;
    }
    if (header.kdf) {
      const p = rememberedP(id);
      if (p) {
        await openWith(null, p);
        return;
      }
      phase = 'password';
      return;
    }
    await openWith(null, null);
  }

  async function openWith(pw: string | null, p: Uint8Array<ArrayBuffer> | null) {
    if (!header || !key) return;
    if (header.kdf && !p && !pw) {
      pwWrong = true;
      return;
    }
    const prev = phase;
    phase = 'decrypting';
    let u: Unlocked;
    try {
      u = await unlock(header, key, pw, p);
      const res = await openPaste(u);
      const s = new PasteStore(u, owner);
      s.plain = res.plain;
      s.version = res.paste.version;
      s.createdAt = res.paste.createdAt;
      s.updatedAt = res.paste.updatedAt;
      s.expiresAt = res.paste.expiresAt;
      s.burn = res.paste.burn;
      s.discussion = res.paste.discussion;
      s.locked = res.paste.locked;
      s.comments = res.comments;
      s.addAttachments(res.paste.attachments);
      wireImages(s);
      s.resolver.ticket = res.paste.imageTicket ?? null;
      store = s;
      if (header.kdf && pw !== null && remember && u.p) secrets.setPw(id, toB64u(u.p));
      if (pw !== null) updatePrefs({ rememberPw: remember });
      burned = res.paste.burn;
      phase = 'ready';
      if (burned) {
        secrets.forget(id);
        history.replaceState(null, '', `/p/${id}`);
      } else live(s);
    } catch (e) {
      if (e instanceof ApiError && e.code === 'unauthorized') {
        if (!header.kdf) {
          // No password involved: the key in the link itself is wrong.
          phase = 'error';
          errorMsg = t('view.decryptFailed');
          return;
        }
        if (p) secrets.setPw(id, null);
        pwWrong = pw !== null;
        phase = header.burn ? 'burnconfirm' : 'password';
        if (prev === 'loading' && !pw) pwWrong = false;
        return;
      }
      if (e instanceof ApiError && e.status === 404) {
        phase = 'notfound';
        return;
      }
      if (e instanceof DecryptError) {
        phase = 'error';
        errorMsg = t('view.decryptFailed');
        return;
      }
      phase = 'error';
      errorMsg = e instanceof ApiError ? e.message : (e as Error).message;
    }
  }

  /** Remote images go through the server's proxy; editors may register new URLs for preview. */
  function wireImages(s: PasteStore) {
    s.resolver.proxyEnabled = () => server.config.imageProxy !== false;
    s.resolver.registrar = async (refs) => {
      if (s.owner) return api.registerImages(s.u.id, { owner: s.owner }, refs);
      if (s.discussion && !s.locked) return api.registerImages(s.u.id, { read: s.u.read }, refs);
      throw new Error('cannot register images');
    };
  }

  function live(s: PasteStore) {
    unsubscribe?.();
    unsubscribe = subscribe(s.u.id, s.u.read, (e) => onEvent(s, e), () => resync(s));
  }

  async function onEvent(s: PasteStore, e: LiveEvent) {
    switch (e.type) {
      case 'comment.created':
      case 'comment.updated':
        await s.upsertEncrypted(e.comment);
        break;
      case 'comment.deleted':
        s.remove(e.id);
        break;
      case 'attachment.created':
        s.addAttachments([e.attachment]);
        break;
      case 'paste.locked':
        s.locked = e.locked;
        break;
      case 'paste.updated':
        if (e.version > s.version) {
          try {
            const v = await api.version(s.u.id, s.u.read, e.version);
            const plain = await decryptBody(s.u, v.content);
            s.plain = plain;
            s.version = e.version;
            s.updatedAt = e.updatedAt;
            if (!editing) toast(t('live.updated', { n: e.version }));
          } catch {
            /* resync will catch up */
          }
        }
        break;
      case 'paste.deleted':
        unsubscribe?.();
        secrets.forget(s.u.id);
        phase = 'destroyed';
        break;
    }
  }

  async function resync(s: PasteStore) {
    try {
      const r = await openPaste(s.u);
      s.plain = r.plain;
      s.version = r.paste.version;
      s.updatedAt = r.paste.updatedAt;
      s.locked = r.paste.locked;
      s.comments = r.comments;
      s.addAttachments(r.paste.attachments);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) phase = 'destroyed';
    }
  }

  // ------------------------------------------------------------ actions

  const link = () => (store && key ? shareLink(id, key) : location.href);

  function copyOwnerLink() {
    if (!owner || !key) return;
    copy(ownerLink(id, key, owner), t('copy.ownerLink.warn'));
  }

  function downloadMd() {
    if (!store) return;
    const { title, body } = store.plain;
    const text = (title ? `# ${title}\n\n` : '') + body;
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([text], { type: 'text/markdown;charset=utf-8' }));
    a.download = (title || id).replace(/[\\/:*?"<>|]+/g, '_').slice(0, 80) + '.md';
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }

  function startEdit() {
    if (!store || !owner || store.burn) return;
    editTitle = store.plain.title;
    editBody = store.plain.body;
    editBase = store.version;
    editFiles = new LocalFiles(store.atts, true);
    editUploaded.clear();
    editing = true;
  }

  function cancelEdit() {
    editing = false;
    editFiles?.clear();
    editFiles = null;
  }

  async function saveEdit() {
    if (!store || !owner || saving) return;
    saving = true;
    try {
      for (const f of editFiles?.referenced(editBody) ?? []) {
        if (editUploaded.has(f.id)) continue; // already sent by an earlier, failed save
        await uploadFile(store.u, { owner }, f, null);
        editUploaded.add(f.id);
        store.atts.add(f);
      }
      const plain: BodyPlain = { v: 1, title: editTitle.trim(), body: editBody };
      const r = await updatePaste(store.u, owner, plain, editBase);
      store.plain = plain;
      store.version = r.version;
      store.updatedAt = r.updatedAt;
      editing = false;
      editFiles = null;
      toast(t('edit.saved', { n: r.version }));
    } catch (e) {
      if (e instanceof ApiError && e.code === 'conflict') toast(t('edit.conflict'), 'error', 6000);
      else toastError(e);
    } finally {
      saving = false;
    }
  }

  async function addEditFiles(input: File[]): Promise<string[]> {
    if (!editFiles) return [];
    const { files, errors } = await stageFiles(input, server.config.maxAttachment, server.config.maxPasteAttachments, 0);
    for (const e of errors) toast(e, 'error', 4000);
    for (const f of files) editFiles.add(f);
    return files.map(attachmentMarkdown);
  }

  async function pickEditFiles() {
    const list = Array.from(editFileInput?.files ?? []);
    if (editFileInput) editFileInput.value = '';
    const s = await addEditFiles(list);
    if (s.length) editor?.insert(s.join('\n'));
  }

  async function toggleLock() {
    if (!store || !owner) return;
    try {
      const r = await api.lock(id, owner, !store.locked);
      store.locked = r.locked;
    } catch (e) {
      toastError(e);
    }
  }

  async function destroy() {
    if (!owner) return;
    const ok = await confirmDialog({ title: t('destroy.title'), body: t('destroy.body'), confirm: t('destroy.confirm'), danger: true });
    if (!ok) return;
    try {
      await api.destroy(id, owner);
      unsubscribe?.();
      secrets.forget(id);
      phase = 'destroyed';
    } catch (e) {
      toastError(e);
    }
  }

  const historySource = $derived.by((): HistorySource | null => {
    const s = store;
    if (!s) return null;
    const asText = (p: BodyPlain) => (p.title ? `# ${p.title}\n\n` : '') + p.body;
    const plainOf = async (v: number) => decryptBody(s.u, (await api.version(s.u.id, s.u.read, v)).content);
    return {
      list: () => api.versions(s.u.id, s.u.read),
      text: async (v) => asText(await plainOf(v)),
      restore: owner
        ? async (v) => {
            const plain = await plainOf(v);
            const r = await updatePaste(s.u, owner!, plain, s.version);
            s.plain = plain;
            s.version = r.version;
            s.updatedAt = r.updatedAt;
            toast(t('history.restored', { n: r.version }));
          }
        : undefined,
    };
  });

  const menuItems = $derived.by((): MenuItem[] => {
    if (!store) return [];
    const items: MenuItem[] = [];
    if (owner && !store.burn) items.push({ label: t('menu.edit'), icon: 'edit', run: startEdit });
    if (!store.burn) items.push({ label: t('menu.history'), icon: 'history', run: () => (historyOpen = true) });
    items.push({ label: showSource ? t('menu.rendered') : t('menu.source'), icon: 'code', run: () => (showSource = !showSource) });
    items.push({ label: t('menu.downloadMd'), icon: 'download', run: downloadMd });
    if (owner && !store.burn) {
      items.push({ separator: true });
      items.push({ label: t('copy.ownerLink'), icon: 'key', run: copyOwnerLink });
      if (store.discussion) items.push({ label: store.locked ? t('menu.unlock') : t('menu.lock'), icon: store.locked ? 'unlock' : 'lock', run: toggleLock });
      items.push({ label: t('menu.destroy'), icon: 'trash', danger: true, run: destroy });
    }
    return items;
  });

  const unregister = provideCommands('view', () => {
    if (phase !== 'ready' || !store) return [];
    return [
      ...(!store.burn ? [{ id: 'copylink', label: t('copy.link'), hint: `${modKey}+Shift+C`, run: () => copy(link()) }] : []),
      { id: 'copytext', label: t('copy.text'), run: () => copy(store!.plain.body) },
      ...menuItems.filter((m): m is Exclude<MenuItem, { separator: true }> => !('separator' in m)).map((m, i) => ({ id: 'm' + i, label: m.label, run: m.run })),
    ];
  });

  function onKey(e: KeyboardEvent) {
    if (editing && (e.ctrlKey || e.metaKey) && e.key === 'Enter' && !e.defaultPrevented) {
      e.preventDefault();
      saveEdit();
      return;
    }
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === 'c' && phase === 'ready' && store && !store.burn) {
      e.preventDefault();
      copy(link());
    }
  }

  const editContext = $derived<LiveContext>({ atts: editFiles ?? store?.atts ?? null, labels: labels() });
  const viewTitle = $derived(store?.plain.title || '');
</script>

<svelte:window onkeydown={onKey} />

{#if phase === 'loading' || phase === 'decrypting'}
  <div class="state"><span class="spinner" aria-hidden="true"></span>{t('view.decrypting')}</div>
{:else if phase === 'badlink'}
  <div class="state card"><Icon name="alert" size={22} /><p>{t('view.badLink')}</p><button class="btn" onclick={() => navigate('/')}><Icon name="plus" />{t('nav.new')}</button></div>
{:else if phase === 'notfound'}
  <div class="state card"><Icon name="alert" size={22} /><p>{t('view.notFound')}</p><button class="btn" onclick={() => navigate('/')}><Icon name="plus" />{t('nav.new')}</button></div>
{:else if phase === 'destroyed'}
  <div class="state card"><Icon name="trash" size={22} /><p>{t('destroyed')}</p><button class="btn" onclick={() => navigate('/')}><Icon name="plus" />{t('nav.new')}</button></div>
{:else if phase === 'error'}
  <div class="state card"><Icon name="alert" size={22} /><p>{errorMsg || t('error.generic', { msg: '' })}</p></div>
{:else if phase === 'burnconfirm' || phase === 'password'}
  <form
    class="gate card"
    onsubmit={(e) => {
      e.preventDefault();
      openWith(header?.kdf ? password : null, null);
    }}
  >
    <div class="gate-icon" class:burn={phase === 'burnconfirm'}><Icon name={phase === 'burnconfirm' ? 'flame' : 'lock'} size={22} /></div>
    <h1>{phase === 'burnconfirm' ? t('view.burn.title') : t('unlock.title')}</h1>
    <p class="muted">{phase === 'burnconfirm' ? t('view.burn.body') : t('unlock.body')}</p>
    {#if header?.kdf}
      <PasswordField bind:value={password} placeholder={t('password.label')} autofocus invalid={pwWrong} autocomplete="off" />
      {#if pwWrong}<p class="err">{t('unlock.wrong')}</p>{/if}
      {#if phase === 'password'}<label class="check"><input type="checkbox" bind:checked={remember} />{t('remember')}</label>{/if}
    {/if}
    <button class="btn {phase === 'burnconfirm' ? 'danger' : 'primary'} wide" type="submit" disabled={!!header?.kdf && !password}>
      <Icon name={phase === 'burnconfirm' ? 'flame' : 'unlock'} />{phase === 'burnconfirm' ? t('view.burn.open') : t('unlock.button')}
    </button>
  </form>
{:else if phase === 'ready' && store}
  {#if burned}
    <div class="notice warn banner"><Icon name="flame" />{t('view.burn.gone')}</div>
  {/if}
  {#if justCreated && key}
    <div class="share card">
      <div class="share-row">
        <Icon name="check" />
        <strong>{t('created.title')}</strong>
        <span class="muted">{t('created.keep', { pw: createdHasPassword ? t('created.keep.pw') : '' })}</span>
      </div>
      <div class="share-row">
        <input class="input mono" readonly value={link()} onfocus={(e) => e.currentTarget.select()} aria-label={t('copy.link')} />
        <button class="btn primary" onclick={() => copy(link())}><Icon name="copy" />{t('copy.link')}</button>
      </div>
      {#if createdHasPassword}<p class="hint"><Icon name="key" />{t('password.sendSeparately')}</p>{/if}
    </div>
  {/if}

  <article class="paste card">
    {#if editing}
      <div class="edit-head">
        <input class="title-input" bind:value={editTitle} placeholder={t('new.title')} aria-label={t('new.title')} maxlength="200" />
        <div class="seg small">
          <button class:on={prefs.editor === 'live'} onclick={() => updatePrefs({ editor: 'live' })}>{t('mode.live')}</button>
          <button class:on={prefs.editor === 'source'} onclick={() => updatePrefs({ editor: 'source' })}>{t('mode.source')}</button>
        </div>
        <button class="btn ghost sm icon" title={t('attach')} aria-label={t('attach')} onclick={() => editFileInput?.click()}><Icon name="paperclip" /></button>
        <input bind:this={editFileInput} type="file" multiple hidden onchange={pickEditFiles} />
      </div>
      <div class="edit-body">
        <Editor
          bind:value={editBody}
          bind:handle={editor}
          mode={prefs.editor}
          context={editContext}
          autofocus
          minLines={10}
          onsubmit={saveEdit}
          ontogglemode={() => updatePrefs({ editor: prefs.editor === 'live' ? 'source' : 'live' })}
          onfiles={addEditFiles}
        />
      </div>
      <div class="edit-foot">
        <span class="muted kbdhint"><kbd>{modKey}</kbd> <kbd>Enter</kbd></span>
        <button class="btn" onclick={cancelEdit}>{t('edit.cancel')}</button>
        <button class="btn primary" onclick={saveEdit} disabled={saving}><Icon name="check" />{saving ? t('edit.saving') : t('edit.save')}</button>
      </div>
    {:else}
      <header class="paste-head">
        <div class="titles">
          <h1 class:untitled={!viewTitle}>{viewTitle || t('view.untitled')}</h1>
          <div class="meta muted">
            <span title={absTime(store.createdAt)}>{t('meta.created', { time: relTime(store.createdAt) })}</span>
            {#if !store.burn}
              <span>·</span>
              {#if store.expiresAt}<span title={absTime(store.expiresAt)}>{t('meta.expires', { time: relTime(store.expiresAt) })}</span>{:else}<span>{t('meta.never')}</span>{/if}
            {/if}
            {#if store.version > 1}
              <span>·</span>
              <button class="edited" onclick={() => (historyOpen = true)} title={absTime(store.updatedAt)}>{t('meta.edited')} ({t('meta.version', { n: store.version })})</button>
            {/if}
            {#if owner && !store.burn}<span class="owner-badge"><Icon name="key" size={12} />{t('meta.owner')}</span>{/if}
          </div>
        </div>
        <div class="head-actions">
          <button class="btn sm" onclick={() => copy(store!.plain.body)} title={t('copy.text')}><Icon name="copy" /><span class="lbl">{t('copy.text')}</span></button>
          {#if !store.burn}
            <button class="btn sm" onclick={() => copy(link())} title={t('copy.link')}><Icon name="link" /><span class="lbl">{t('copy.link')}</span></button>
          {/if}
          {#if owner && !store.burn}
            <button class="btn sm ghost icon" onclick={startEdit} title={t('menu.edit')} aria-label={t('menu.edit')}><Icon name="edit" /></button>
          {/if}
          <Menu items={menuItems} label={t('menu.more')} />
        </div>
      </header>
      <div class="paste-body">
        {#if showSource}
          <pre class="source">{store.plain.body}</pre>
        {:else}
          <Markdown source={store.plain.body} atts={store.atts} />
        {/if}
      </div>
    {/if}
  </article>

  {#if store.discussion && !store.burn}
    <Discussion {store} />
  {/if}

  <History open={historyOpen} onclose={() => (historyOpen = false)} source={historySource} atts={store.atts} />
{/if}

<style>
  .state {
    display: grid;
    justify-items: center;
    gap: 12px;
    padding: 40px 24px;
    text-align: center;
    color: var(--text-2);
  }
  .state p {
    margin: 0;
    max-width: 440px;
  }
  .spinner {
    width: 22px;
    height: 22px;
    border-radius: 50%;
    border: 2px solid var(--border-strong);
    border-top-color: var(--accent);
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .gate {
    max-width: 420px;
    margin: 40px auto 0;
    padding: 28px 24px;
    display: grid;
    gap: 12px;
    justify-items: center;
    text-align: center;
  }
  .gate h1 {
    margin: 0;
    font-size: 19px;
  }
  .gate p {
    margin: 0;
    font-size: 14px;
  }
  .gate-icon {
    width: 44px;
    height: 44px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    background: var(--accent-2);
    color: var(--accent);
  }
  .gate-icon.burn {
    background: var(--danger-2);
    color: var(--danger);
  }
  .gate .err {
    color: var(--danger);
    font-size: 13px;
  }
  .wide {
    width: 100%;
    height: 38px;
  }
  .banner {
    display: flex;
    gap: 8px;
    align-items: center;
    margin-bottom: 12px;
  }
  .share {
    padding: 14px 16px;
    margin-bottom: 14px;
    display: grid;
    gap: 10px;
    border-color: color-mix(in srgb, var(--accent) 35%, var(--border));
  }
  .share-row {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    font-size: 14px;
  }
  .share-row :global(.i) {
    color: var(--ok);
  }
  .share-row .input {
    flex: 1;
    min-width: 0;
  }
  .share-row .btn :global(.i) {
    color: inherit;
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
    margin: 0;
  }
  .paste {
    overflow: visible;
  }
  .paste-head {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    justify-content: space-between;
    padding: 16px 16px 12px 22px;
    border-bottom: 1px solid var(--border);
  }
  .titles {
    min-width: 0;
  }
  h1 {
    margin: 0;
    font-size: 21px;
    font-weight: 650;
    letter-spacing: -0.015em;
    line-height: 1.3;
    overflow-wrap: anywhere;
  }
  h1.untitled {
    color: var(--muted);
    font-weight: 550;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    font-size: 13px;
    margin-top: 4px;
  }
  .edited {
    border: none;
    background: none;
    padding: 0;
    color: inherit;
    font-size: inherit;
    text-decoration: underline dotted;
    text-underline-offset: 3px;
    cursor: pointer;
  }
  .owner-badge {
    display: inline-flex;
    gap: 4px;
    align-items: center;
    margin-left: 4px;
    padding: 0 7px;
    border-radius: 99px;
    background: var(--accent-2);
    color: var(--accent);
    font-size: 11.5px;
    font-weight: 550;
  }
  .head-actions {
    display: flex;
    gap: 4px;
    align-items: center;
    flex: none;
  }
  .paste-body {
    padding: 18px 22px 24px;
  }
  .source {
    margin: 0;
    font: 13.5px/1.6 var(--font-mono);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .edit-head {
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 10px 12px 6px 22px;
    border-bottom: 1px solid var(--border);
  }
  .title-input {
    flex: 1;
    min-width: 0;
    border: none;
    outline: none;
    background: none;
    font-size: 18px;
    font-weight: 620;
    padding: 6px 0;
  }
  .edit-body {
    padding: 10px 22px;
  }
  .edit-foot {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    align-items: center;
    padding: 10px 14px;
    border-top: 1px solid var(--border);
  }
  .kbdhint {
    font-size: 12px;
    margin-right: auto;
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
  @media (max-width: 640px) {
    .paste-head {
      flex-direction: column;
      padding: 14px 14px 10px;
    }
    .head-actions {
      width: 100%;
    }
    .head-actions .lbl {
      display: none;
    }
    .paste-body {
      padding: 14px;
    }
    .edit-body {
      padding: 8px 12px;
    }
    .edit-head {
      padding-left: 12px;
    }
    .kbdhint {
      display: none;
    }
  }
</style>
