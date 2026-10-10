import { describe, expect, it } from 'vitest';
import { fromB64u, randomBytes, toB64u } from './b64';
import { DecryptError, deriveKeys, derivePasswordKey, seal, sealJSON, unseal, unsealJSON } from './crypto';
import { parseFragment } from './paste';

const FAST_KDF = { alg: 'argon2id' as const, m: 1024, t: 1, p: 1 };

describe('b64u', () => {
  it('round-trips without padding', () => {
    for (const n of [0, 1, 2, 3, 16, 32, 100000]) {
      const b = randomBytes(n);
      const s = toB64u(b);
      expect(s).not.toMatch(/[=+/]/);
      expect(fromB64u(s)).toEqual(b);
    }
  });
  it('rejects garbage', () => {
    expect(() => fromB64u('ab$c')).toThrow();
  });
});

describe('keys', () => {
  it('password changes both keys and read auth', async () => {
    const k = randomBytes(32);
    const salt = randomBytes(16);
    const a = await deriveKeys(k, null, salt);
    const p = await derivePasswordKey('hunter2', salt, FAST_KDF);
    const b = await deriveKeys(k, p, salt);
    expect(toB64u(a.readAuth)).not.toEqual(toB64u(b.readAuth));
    const env = await sealJSON(b.encKey, 'body', 'body', { x: 1 });
    await expect(unseal(a.encKey, 'body', 'body', env)).rejects.toBeInstanceOf(DecryptError);
    expect(await unsealJSON(b.encKey, 'body', 'body', env)).toEqual({ x: 1 });
  });

  it('is deterministic for the same inputs', async () => {
    const k = randomBytes(32);
    const salt = randomBytes(16);
    const p1 = await derivePasswordKey('pässword', salt, FAST_KDF);
    const p2 = await derivePasswordKey('pässword', salt, FAST_KDF);
    expect(p1).toEqual(p2);
    const a = await deriveKeys(k, p1, salt);
    const b = await deriveKeys(k, p2, salt);
    expect(a.readAuth).toEqual(b.readAuth);
  });
});

describe('envelope', () => {
  it('binds kind and object id via AAD', async () => {
    const { encKey } = await deriveKeys(randomBytes(32), null, randomBytes(16));
    const env = await sealJSON(encKey, 'comment', 'abc', { nick: 'a', body: 'b' });
    await expect(unseal(encKey, 'comment', 'abd', env)).rejects.toBeInstanceOf(DecryptError);
    await expect(unseal(encKey, 'body', 'abc', env)).rejects.toBeInstanceOf(DecryptError);
    expect(await unsealJSON(encKey, 'comment', 'abc', env)).toEqual({ nick: 'a', body: 'b' });
  });

  it('compresses text and leaves binary alone', async () => {
    const { encKey } = await deriveKeys(randomBytes(32), null, randomBytes(16));
    const text = 'hello world '.repeat(1000);
    const env = await sealJSON(encKey, 'body', 'body', { body: text });
    expect(env[0]).toBe(1);
    expect(env[1]).toBe(1);
    expect(env.length).toBeLessThan(text.length / 10);
    const bin = randomBytes(5000);
    const env2 = await seal(encKey, 'attdata', 'x', bin, false);
    expect(env2[1]).toBe(0);
    expect(await unseal(encKey, 'attdata', 'x', env2)).toEqual(bin);
  });

  it('detects tampering', async () => {
    const { encKey } = await deriveKeys(randomBytes(32), null, randomBytes(16));
    const env = await sealJSON(encKey, 'body', 'body', { a: 1 });
    env[env.length - 1] ^= 1;
    await expect(unseal(encKey, 'body', 'body', env)).rejects.toBeInstanceOf(DecryptError);
  });
});

describe('fragment', () => {
  it('parses share and owner links', () => {
    const k = toB64u(randomBytes(32));
    const o = toB64u(randomBytes(32));
    expect(parseFragment('#' + k)?.owner).toBeNull();
    expect(parseFragment('#' + k + '.' + o)?.owner).toBe(o);
    expect(parseFragment('#' + k.slice(1))).toBeNull();
    expect(parseFragment('#' + k + '.short')).toBeNull();
    expect(parseFragment('')).toBeNull();
  });
});
