import { describe, expect, it } from 'vitest';
import { normalizeExt, remoteImages } from './markdown';

describe('remote images', () => {
  it('finds http(s) images only, canonicalized and deduplicated', () => {
    const md = [
      '![a](https://example.com/a.png)',
      '![b](att:AAAAAAAAAAAAAAAAAAAAAA)',
      '![c](ftp://example.com/c.png)',
      '[not an image](https://example.com/link.png)',
      '![d](HTTPS://Example.COM/a.png)',
      '- ![e](<https://example.com/with space.png>)',
      '`![f](https://example.com/in-code.png)`',
    ].join('\n\n');
    expect(remoteImages(md)).toEqual(['https://example.com/a.png', 'https://example.com/with%20space.png']);
  });

  it('rejects credentials and non-http schemes', () => {
    expect(normalizeExt('https://user:pw@example.com/a.png')).toBeNull();
    expect(normalizeExt('javascript:alert(1)')).toBeNull();
    expect(normalizeExt('https://example.com/a.png')).toBe('https://example.com/a.png');
  });

  it('is stable when applied twice (registration and fetch must agree)', () => {
    const once = normalizeExt('https://example.com/ü/a b.png')!;
    expect(normalizeExt(once)).toBe(once);
  });
});
