// Client-side cryptography. See DESIGN.md §3.
//
//   K        = 32 random bytes (URL fragment)
//   P        = password ? Argon2id(password, salt) : empty
//   encKey   = HKDF-SHA256(K || P, salt, "hushbin/v1/enc")  → AES-256-GCM
//   readAuth = HKDF-SHA256(K || P, salt, "hushbin/v1/read") → sent to server
//
// Envelope: 0x01 | flags | nonce(12) | AES-GCM(ct || tag), flags bit0 = deflate-raw.

import { argon2id } from 'hash-wasm';
import { randomBytes } from './b64';

export type Bytes = Uint8Array<ArrayBuffer>;

export type Kdf = { alg: 'argon2id'; m: number; t: number; p: number };
export const DEFAULT_KDF: Kdf = { alg: 'argon2id', m: 65536, t: 3, p: 1 };

export type Kind = 'body' | 'comment' | 'attmeta' | 'attdata';

export type PasteKeys = {
  encKey: CryptoKey;
  readAuth: Bytes;
};

const ENVELOPE_VERSION = 1;
const FLAG_DEFLATE = 1;
const enc = new TextEncoder();
const dec = new TextDecoder();

export async function derivePasswordKey(password: string, salt: Bytes, kdf: Kdf): Promise<Bytes> {
  if (kdf.alg !== 'argon2id') throw new Error('unsupported kdf');
  const out = await argon2id({
    password: enc.encode(password.normalize('NFC')),
    salt,
    parallelism: kdf.p,
    iterations: kdf.t,
    memorySize: kdf.m,
    hashLength: 32,
    outputType: 'binary',
  });
  return new Uint8Array(out);
}

async function hkdf(ikm: CryptoKey, salt: Bytes, info: string, bits: number): Promise<Bytes> {
  const out = await crypto.subtle.deriveBits(
    { name: 'HKDF', hash: 'SHA-256', salt, info: enc.encode(info) },
    ikm,
    bits,
  );
  return new Uint8Array(out);
}

export async function deriveKeys(k: Bytes, p: Bytes | null, salt: Bytes): Promise<PasteKeys> {
  const ikmBytes = new Uint8Array(k.length + (p?.length ?? 0));
  ikmBytes.set(k, 0);
  if (p) ikmBytes.set(p, k.length);
  const ikm = await crypto.subtle.importKey('raw', ikmBytes, 'HKDF', false, ['deriveBits']);
  const encRaw = await hkdf(ikm, salt, 'hushbin/v1/enc', 256);
  const readAuth = await hkdf(ikm, salt, 'hushbin/v1/read', 256);
  const encKey = await crypto.subtle.importKey('raw', encRaw, 'AES-GCM', false, ['encrypt', 'decrypt']);
  encRaw.fill(0);
  ikmBytes.fill(0);
  return { encKey, readAuth };
}

export async function sha256(data: Bytes): Promise<Bytes> {
  return new Uint8Array(await crypto.subtle.digest('SHA-256', data));
}

function aad(kind: Kind, objectId: string): Bytes {
  return enc.encode(`hb1|${kind}|${objectId}`);
}

async function pipeThrough(data: Bytes, stream: CompressionStream | DecompressionStream): Promise<Bytes> {
  const res = new Response(new Blob([data]).stream().pipeThrough(stream));
  return new Uint8Array(await res.arrayBuffer());
}

export const deflate = (d: Bytes) => pipeThrough(d, new CompressionStream('deflate-raw'));
export const inflate = (d: Bytes) => pipeThrough(d, new DecompressionStream('deflate-raw'));

export async function seal(
  key: CryptoKey,
  kind: Kind,
  objectId: string,
  plaintext: Bytes,
  compress: boolean,
): Promise<Bytes> {
  const body = compress ? await deflate(plaintext) : plaintext;
  const nonce = randomBytes(12);
  const ct = new Uint8Array(
    await crypto.subtle.encrypt({ name: 'AES-GCM', iv: nonce, additionalData: aad(kind, objectId) }, key, body),
  );
  const out = new Uint8Array(2 + 12 + ct.length);
  out[0] = ENVELOPE_VERSION;
  out[1] = compress ? FLAG_DEFLATE : 0;
  out.set(nonce, 2);
  out.set(ct, 14);
  return out;
}

export class DecryptError extends Error {}

export async function unseal(key: CryptoKey, kind: Kind, objectId: string, envelope: Bytes): Promise<Bytes> {
  if (envelope.length < 2 + 12 + 16 || envelope[0] !== ENVELOPE_VERSION) {
    throw new DecryptError('unsupported envelope');
  }
  const flags = envelope[1];
  const nonce = envelope.subarray(2, 14);
  const ct = envelope.subarray(14);
  let pt: Bytes;
  try {
    pt = new Uint8Array(
      await crypto.subtle.decrypt({ name: 'AES-GCM', iv: nonce, additionalData: aad(kind, objectId) }, key, ct),
    );
  } catch {
    throw new DecryptError('decryption failed');
  }
  return flags & FLAG_DEFLATE ? inflate(pt) : pt;
}

export async function sealJSON(key: CryptoKey, kind: Kind, objectId: string, value: unknown): Promise<Bytes> {
  return seal(key, kind, objectId, enc.encode(JSON.stringify(value)), kind !== 'attmeta');
}

export async function unsealJSON<T>(key: CryptoKey, kind: Kind, objectId: string, envelope: Bytes): Promise<T> {
  return JSON.parse(dec.decode(await unseal(key, kind, objectId, envelope))) as T;
}

// Plaintext shapes.
export type BodyPlain = { v: 1; title: string; body: string };
export type CommentPlain = { v: 1; nick: string; body: string };
export type AttMetaPlain = { v: 1; name: string; type: string; size: number };
