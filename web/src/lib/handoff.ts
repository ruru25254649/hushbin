// Passes freshly created/decrypted state from the create screen to the view
// screen without a second round-trip (and without re-deriving keys).

import type { Unlocked, LocalFile } from './paste';
import type { BodyPlain } from './crypto';
import type { ServerConfig } from './api';

export type Handoff = {
  id: string;
  unlocked: Unlocked;
  plain: BodyPlain;
  files: LocalFile[];
  expiresAt: number | null;
  discussion: boolean;
  hasPassword: boolean;
};

let pending: Handoff | null = null;

export function setHandoff(h: Handoff) {
  pending = h;
}

export function takeHandoff(id: string): Handoff | null {
  const h = pending;
  pending = null;
  return h && h.id === id ? h : null;
}

export const DEFAULT_CONFIG: ServerConfig = {
  version: '',
  maxBody: 2 * 1024 * 1024,
  maxComment: 256 * 1024,
  maxAttachment: 25 * 1024 * 1024,
  maxPasteAttachments: 100 * 1024 * 1024,
  expiries: [300, 3600, 86400, 604800, 2592000, 0],
};
