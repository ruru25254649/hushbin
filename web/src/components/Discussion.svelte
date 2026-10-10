<script lang="ts">
  import Composer from './Composer.svelte';
  import Markdown from './Markdown.svelte';
  import Identicon from './Identicon.svelte';
  import Icon from './Icon.svelte';
  import History, { type HistorySource, type HistoryEntry } from './History.svelte';
  import { t, relTime, absTime } from '../lib/i18n/index.svelte';
  import { confirmDialog, toastError } from '../lib/ui.svelte';
  import { api, type Auth } from '../lib/api';
  import { authorToken, decryptComment, editComment, postComment, tagFor, type PlainComment } from '../lib/paste';
  import { secrets } from '../lib/storage';
  import type { PasteStore } from '../lib/pasteStore.svelte';

  let { store }: { store: PasteStore } = $props();

  // A paste's store (and so its owner token and id) never changes for this component.
  const { owner, id } = (() => ({ owner: store.owner, id: store.u.id }))();
  let myTag = $state<string | null>(null);
  let replyTo = $state<PlainComment | null>(null);
  let editingId = $state<string | null>(null);
  let historyFor = $state<PlainComment | null>(null);

  $effect(() => {
    const tok = secrets.author(id);
    if (tok) tagFor(tok).then((tag) => (myTag = tag));
  });

  const threads = $derived(store.threads());
  const count = $derived(store.comments.filter((c) => !c.deleted).length);
  const canWrite = $derived(!store.locked || !!owner);

  function auth(): Auth {
    if (owner) return { owner };
    const tok = authorToken(id);
    if (!myTag) tagFor(tok).then((tag) => (myTag = tag));
    return { author: tok };
  }

  const mine = (c: PlainComment) => (c.author === 'owner' ? !!owner : !!myTag && c.tag === myTag);
  const canEdit = (c: PlainComment) => !c.deleted && !c.broken && mine(c) && (!store.locked || !!owner);
  const canDelete = (c: PlainComment) => !c.deleted && (mine(c) || !!owner);

  async function post(body: string, nick: string, commentId: string | null) {
    const c = await postComment(store.u, auth(), commentId!, replyTo?.id ?? null, nick, body);
    store.upsert({ ...c, nick, body });
    replyTo = null;
  }

  async function saveEdit(c: PlainComment, body: string) {
    const updated = await editComment(store.u, auth(), c, c.nick, body);
    store.upsert({ ...updated, nick: c.nick, body });
    editingId = null;
  }

  async function remove(c: PlainComment) {
    const ok = await confirmDialog({ title: t('discussion.delete'), body: t('discussion.deleteConfirm'), confirm: t('discussion.delete'), danger: true });
    if (!ok) return;
    try {
      const a: Auth = mine(c) ? auth() : { owner: owner! };
      await api.deleteComment(id, { ...a, read: store.u.read }, c.id);
      const hasReplies = !c.parentId && store.comments.some((x) => x.parentId === c.id);
      if (hasReplies) store.upsert({ ...c, deleted: true, content: null, body: '', nick: '' });
      else {
        store.remove(c.id);
        const parent = c.parentId ? store.comments.find((x) => x.id === c.parentId) : null;
        if (parent?.deleted && !store.comments.some((x) => x.parentId === parent.id)) store.remove(parent.id);
      }
    } catch (e) {
      toastError(e);
    }
  }

  function commentHistory(c: PlainComment): HistorySource {
    let loaded: Promise<{ list: HistoryEntry[]; texts: Map<number, string> }> | null = null;
    const load = () =>
      (loaded ??= api.commentVersions(id, store.u.read, c.id).then(async (vs) => {
        const texts = new Map<number, string>();
        for (const v of vs) {
          const p = await decryptComment(store.u, { ...c, content: v.content, deleted: false });
          texts.set(v.version, p.body);
        }
        return { list: vs.map((v) => ({ version: v.version, createdAt: v.createdAt })), texts };
      }));
    return {
      list: async () => (await load()).list,
      text: async (v: number) => (await load()).texts.get(v) ?? '',
    };
  }

  const historySource = $derived(historyFor ? commentHistory(historyFor) : null);
</script>

{#snippet comment(c: PlainComment, isReply: boolean)}
  <article class="msg" class:reply={isReply} class:deleted={c.deleted} id="c-{c.id}">
    {#if c.deleted}
      <div class="tomb muted"><Icon name="trash" />{t('discussion.deleted')}</div>
    {:else}
      <Identicon tag={c.tag} size={isReply ? 24 : 30} />
      <div class="main">
        <header>
          <span class="nick">{c.nick || t('discussion.anon')}</span>
          {#if c.author === 'owner'}<span class="badge">{t('discussion.author')}</span>{/if}
          {#if mine(c)}<span class="you muted">({t('discussion.you')})</span>{/if}
          <time class="muted" datetime={new Date(c.createdAt).toISOString()} title={absTime(c.createdAt)}>{relTime(c.createdAt)}</time>
          {#if c.version > 1}
            <button class="edited" onclick={() => (historyFor = c)} title={absTime(c.updatedAt)}>{t('meta.edited')}</button>
          {/if}
          <span class="spacer"></span>
          <span class="acts">
            {#if !isReply && canWrite && !c.broken}
              <button class="btn ghost sm icon" title={t('discussion.reply')} aria-label={t('discussion.reply')} onclick={() => ((replyTo = c), (editingId = null))}><Icon name="reply" /></button>
            {/if}
            {#if canEdit(c)}
              <button class="btn ghost sm icon" title={t('discussion.edit')} aria-label={t('discussion.edit')} onclick={() => ((editingId = c.id), (replyTo = null))}><Icon name="edit" /></button>
            {/if}
            {#if canDelete(c)}
              <button class="btn ghost sm icon" title={t('discussion.delete')} aria-label={t('discussion.delete')} onclick={() => remove(c)}><Icon name="trash" /></button>
            {/if}
          </span>
        </header>
        {#if editingId === c.id}
          <Composer {store} auth={auth()} initial={c.body} editing={c} autofocus onsubmit={(b) => saveEdit(c, b)} oncancel={() => (editingId = null)} />
        {:else if c.broken}
          <p class="notice error">{t('discussion.unreadable')}</p>
        {:else}
          <Markdown source={c.body} atts={store.atts} />
        {/if}
      </div>
    {/if}
  </article>
{/snippet}

<section class="discussion" aria-labelledby="disc-h">
  <h2 id="disc-h">
    <Icon name="message" />{t('discussion.title')}
    {#if count}<span class="count">{count}</span>{/if}
    {#if store.locked}<span class="locked"><Icon name="lock" size={13} />{t('discussion.locked')}</span>{/if}
  </h2>

  {#if !threads.length}
    <p class="muted empty">{t('discussion.empty')}</p>
  {/if}

  {#each threads as th (th.root.id)}
    <div class="thread">
      {@render comment(th.root, false)}
      {#if th.replies.length || replyTo?.id === th.root.id}
        <div class="replies">
          {#each th.replies as r (r.id)}{@render comment(r, true)}{/each}
          {#if replyTo?.id === th.root.id}
            <Composer {store} auth={auth()} replyTo={th.root} autofocus onsubmit={post} oncancel={() => (replyTo = null)} />
          {/if}
        </div>
      {/if}
    </div>
  {/each}

  {#if canWrite}
    {#if !replyTo}
      <div class="new"><Composer {store} auth={auth()} onsubmit={post} /></div>
    {/if}
  {:else}
    <p class="notice">{t('discussion.locked')}</p>
  {/if}
</section>

<History open={!!historyFor} onclose={() => (historyFor = null)} source={historySource} atts={store.atts} />

<style>
  .discussion {
    margin-top: 28px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 16px;
    font-weight: 620;
    margin: 0 0 12px;
  }
  .count {
    font-size: 12px;
    font-weight: 600;
    padding: 1px 7px;
    border-radius: 99px;
    background: var(--surface-2);
    color: var(--text-2);
  }
  .locked {
    display: inline-flex;
    gap: 4px;
    align-items: center;
    font-size: 12px;
    font-weight: 500;
    color: var(--warn);
  }
  .empty {
    margin: 0 0 14px;
    font-size: 14px;
  }
  .thread {
    border: 1px solid var(--border);
    background: var(--surface);
    border-radius: var(--radius);
    margin-bottom: 10px;
    overflow: hidden;
  }
  .msg {
    display: flex;
    gap: 12px;
    padding: 12px 14px;
  }
  .msg.reply {
    padding: 10px 14px 10px 10px;
  }
  .main {
    flex: 1;
    min-width: 0;
  }
  header {
    display: flex;
    align-items: center;
    gap: 7px;
    flex-wrap: wrap;
    font-size: 13px;
    min-height: 28px;
    margin-bottom: 2px;
  }
  .nick {
    font-weight: 600;
    font-size: 14px;
  }
  .badge {
    font-size: 11px;
    font-weight: 600;
    padding: 1px 7px;
    border-radius: 99px;
    background: var(--accent-2);
    color: var(--accent);
  }
  .edited {
    border: none;
    background: none;
    padding: 0;
    font-size: 12px;
    color: var(--muted);
    text-decoration: underline dotted;
    text-underline-offset: 3px;
    cursor: pointer;
  }
  .spacer {
    flex: 1;
  }
  .acts {
    display: flex;
    gap: 2px;
    opacity: 0.55;
    transition: opacity 0.12s;
  }
  .msg:hover .acts,
  .acts:focus-within {
    opacity: 1;
  }
  @media (hover: none) {
    .acts {
      opacity: 1;
    }
  }
  .main :global(.markdown) {
    font-size: 15px;
  }
  .replies {
    border-top: 1px solid var(--border);
    background: var(--bg);
    padding: 4px 0 4px 34px;
    display: grid;
    gap: 0;
  }
  .replies > :global(.composer) {
    padding: 8px 14px 12px 10px;
  }
  .tomb {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-style: italic;
  }
  .new {
    margin-top: 14px;
  }
  @media (max-width: 560px) {
    .replies {
      padding-left: 14px;
    }
    .msg {
      gap: 10px;
      padding: 10px 12px;
    }
  }
</style>
