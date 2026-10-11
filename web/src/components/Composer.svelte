<script lang="ts">
  import { onDestroy } from 'svelte';
  import Editor from './Editor.svelte';
  import Icon from './Icon.svelte';
  import { t, renderLabels } from '../lib/i18n/index.svelte';
  import { prefs, updatePrefs, server, toast, toastError, modKey } from '../lib/ui.svelte';
  import { attachmentMarkdown, uploadFile, type PlainComment } from '../lib/paste';
  import { LocalFiles, stageFiles } from '../lib/local';
  import { secrets } from '../lib/storage';
  import { newId } from '../lib/b64';
  import type { PasteStore } from '../lib/pasteStore.svelte';
  import type { EditorHandle } from '../lib/editor/editor';
  import type { LiveContext } from '../lib/editor/livePreview';
  import type { Auth } from '../lib/api';

  let {
    store,
    auth,
    initial = '',
    editing = null,
    replyTo = null,
    autofocus = false,
    onsubmit,
    oncancel,
  }: {
    store: PasteStore;
    auth: Auth;
    initial?: string;
    editing?: PlainComment | null;
    replyTo?: PlainComment | null;
    autofocus?: boolean;
    /** Upload has finished for `files`; persist the message. */
    onsubmit: (body: string, nick: string, commentId: string | null) => Promise<void>;
    oncancel?: () => void;
  } = $props();

  // Props are fixed for a composer's lifetime (it is re-created per target).
  const init = (() => ({ body: initial, nick: secrets.nick(store.u.id) ?? prefs.nick ?? '', atts: store.atts }))();
  let body = $state(init.body);
  let nick = $state(init.nick);
  let busy = $state(false);
  let editor = $state<EditorHandle>();
  let fileInput: HTMLInputElement;
  const staged = new LocalFiles(init.atts, true);

  const context: LiveContext = {
    atts: staged,
    get labels() {
      return renderLabels();
    },
  };

  async function addFiles(input: File[]): Promise<string[]> {
    const { files, errors } = await stageFiles(input, server.config.maxAttachment, server.config.maxPasteAttachments, 0);
    for (const e of errors) toast(e, 'error', 4000);
    for (const f of files) staged.add(f);
    return files.map(attachmentMarkdown);
  }

  async function pick() {
    const list = Array.from(fileInput.files ?? []);
    fileInput.value = '';
    const s = await addFiles(list);
    if (s.length) editor?.insert(s.join('\n'));
  }

  async function send() {
    if (busy || !body.trim()) return;
    busy = true;
    try {
      const commentId = editing?.id ?? newId();
      for (const f of staged.referenced(body)) {
        await uploadFile(store.u, auth, f, commentId);
        store.atts.add(f);
      }
      const n = nick.trim().slice(0, 64);
      secrets.setNick(store.u.id, n || null);
      if (n !== prefs.nick) updatePrefs({ nick: n });
      await onsubmit(body, n, commentId);
      body = '';
      staged.files.clear();
    } catch (e) {
      toastError(e);
    } finally {
      busy = false;
    }
  }

  const toggleMode = () => updatePrefs({ editor: prefs.editor === 'live' ? 'source' : 'live' });

  onDestroy(() => staged.clear());
</script>

<div class="composer" class:inline={!!editing || !!replyTo}>
  {#if replyTo}
    <div class="replying">
      <Icon name="reply" />
      <span>{t('discussion.replyTo', { name: replyTo.nick || t('discussion.anon') })}</span>
    </div>
  {/if}
  <div class="box">
    <Editor
      bind:value={body}
      bind:handle={editor}
      mode={prefs.editor}
      {context}
      placeholder={t('discussion.placeholder')}
      label={t('discussion.placeholder')}
      {autofocus}
      minLines={2}
      onsubmit={send}
      ontogglemode={toggleMode}
      onfiles={addFiles}
    />
  </div>
  <div class="bar">
    {#if !editing}
      <input
        class="input nick"
        bind:value={nick}
        placeholder={t('discussion.nick')}
        aria-label={t('discussion.nick')}
        maxlength="64"
        onkeydown={(e) => {
          if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
            e.preventDefault();
            send();
          }
        }}
      />
    {/if}
    <button class="btn ghost sm icon" title={t('attach')} aria-label={t('attach')} onclick={() => fileInput.click()}><Icon name="paperclip" /></button>
    <input bind:this={fileInput} type="file" multiple hidden onchange={pick} />
    <span class="spacer"></span>
    {#if oncancel}<button class="btn sm" onclick={oncancel}>{t('cancel')}</button>{/if}
    <button class="btn primary sm" disabled={busy || !body.trim()} onclick={send} title="{modKey}+Enter">
      <Icon name={editing ? 'check' : 'send'} />{busy ? t('discussion.sending') : editing ? t('edit.save') : t('discussion.send')}
    </button>
  </div>
</div>

<style>
  .composer {
    display: grid;
    gap: 8px;
  }
  .replying {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--text-2);
  }
  .box {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
    padding: 4px 12px;
  }
  .box:focus-within {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 15%, transparent);
  }
  .box :global(.cm-content) {
    padding-bottom: 6px;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .nick {
    max-width: 200px;
    height: 30px;
    font-size: 13px;
  }
  .spacer {
    flex: 1;
  }
</style>
