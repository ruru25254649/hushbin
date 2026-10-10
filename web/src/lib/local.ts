// Attachments staged in the browser before they are uploaded.

import { readFile, type LocalFile } from './paste';
import type { AttSource } from './render/hydrate';
import { formatBytes } from './render/hydrate';
import { t } from './i18n/index.svelte';

export class LocalFiles implements AttSource {
  files = new Map<string, LocalFile>();
  private urls = new Map<string, string>();
  constructor(private fallback: AttSource | null = null) {}

  add(f: LocalFile) {
    this.files.set(f.id, f);
  }

  resolve(id: string) {
    const f = this.files.get(id);
    if (!f) return this.fallback ? this.fallback.resolve(id) : Promise.reject(new Error('missing'));
    let url = this.urls.get(id);
    if (!url) {
      url = URL.createObjectURL(new Blob([f.data], { type: f.type }));
      this.urls.set(id, url);
    }
    return Promise.resolve({ meta: { name: f.name, type: f.type, size: f.data.length }, url });
  }

  /** Files whose `att:<id>` reference still appears in the text. */
  referenced(text: string): LocalFile[] {
    return [...this.files.values()].filter((f) => text.includes(`att:${f.id}`));
  }

  clear() {
    for (const u of this.urls.values()) URL.revokeObjectURL(u);
    this.urls.clear();
    this.files.clear();
  }
}

export type StageResult = { files: LocalFile[]; errors: string[] };

/** Read dropped/pasted files, enforcing per-file and total limits. */
export async function stageFiles(input: File[], maxEach: number, maxTotal: number, already: number): Promise<StageResult> {
  const files: LocalFile[] = [];
  const errors: string[] = [];
  let total = already;
  for (const f of input) {
    // Envelope adds 30 bytes; leave headroom.
    if (f.size + 64 > maxEach) {
      errors.push(t('attach.tooLarge', { name: f.name, size: formatBytes(maxEach) }));
      continue;
    }
    if (total + f.size > maxTotal) {
      errors.push(t('attach.totalTooLarge', { size: formatBytes(maxTotal) }));
      break;
    }
    total += f.size;
    files.push(await readFile(f));
  }
  return { files, errors };
}
