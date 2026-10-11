// Reactive state for an opened paste, shared by the view and the discussion.

import type { Attachment } from './api';
import type { BodyPlain } from './crypto';
import { AttachmentResolver, decryptComment, type PlainComment, type Unlocked } from './paste';
import { LocalFiles } from './local';

export type Thread = { root: PlainComment; replies: PlainComment[] };

export class PasteStore {
  plain = $state<BodyPlain>({ v: 1, title: '', body: '' });
  version = $state(1);
  createdAt = $state(0);
  updatedAt = $state(0);
  expiresAt = $state<number | null>(null);
  burn = $state(false);
  discussion = $state(false);
  locked = $state(false);
  comments = $state<PlainComment[]>([]);
  attachmentCount = $state(0);

  readonly resolver: AttachmentResolver;
  /** Resolves local (just uploaded) files first, then server attachments. */
  readonly atts: LocalFiles;

  constructor(
    readonly u: Unlocked,
    readonly owner: string | null,
  ) {
    this.resolver = new AttachmentResolver(u);
    this.atts = new LocalFiles(this.resolver);
  }

  addAttachments(list: Attachment[]) {
    this.resolver.add(list);
    this.attachmentCount = this.resolver.list().length;
  }

  threads(): Thread[] {
    const roots = this.comments.filter((c) => !c.parentId).sort((a, b) => a.createdAt - b.createdAt);
    return roots.map((root) => ({
      root,
      replies: this.comments.filter((c) => c.parentId === root.id).sort((a, b) => a.createdAt - b.createdAt),
    }));
  }

  upsert(c: PlainComment) {
    const i = this.comments.findIndex((x) => x.id === c.id);
    if (i >= 0) {
      // Ignore stale events that arrive after a newer local update.
      if (this.comments[i].version > c.version && !c.deleted) return;
      this.comments[i] = c;
    } else {
      this.comments.push(c);
    }
  }

  async upsertEncrypted(c: Parameters<typeof decryptComment>[1]) {
    this.upsert(await decryptComment(this.u, c));
  }

  remove(id: string) {
    this.comments = this.comments.filter((c) => c.id !== id);
  }

  dispose() {
    this.atts.clear();
    this.resolver.dispose();
  }
}
