import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  isOpencodeGoAnonymousFreeKey,
  opencodeGoEffectiveApiKey,
} from '@/features/providers/opencodeGoFreeTier';

describe('opencodeGoFreeTier', () => {
  test('only the literal public key selects the anonymous tier', () => {
    expect(isOpencodeGoAnonymousFreeKey('public')).toBe(true);
    expect(isOpencodeGoAnonymousFreeKey('PUBLIC')).toBe(false);
    expect(isOpencodeGoAnonymousFreeKey('opencode-go-sk-1')).toBe(false);
    expect(isOpencodeGoAnonymousFreeKey('')).toBe(false);
  });

  test('effective key: typed value wins, stored value is the edit-mode fallback', () => {
    expect(opencodeGoEffectiveApiKey(' public ', 'stored')).toBe('public');
    expect(opencodeGoEffectiveApiKey('', ' public ')).toBe('public');
    expect(opencodeGoEffectiveApiKey('typed', 'stored')).toBe('typed');
    expect(opencodeGoEffectiveApiKey('', undefined)).toBe('');
    expect(isOpencodeGoAnonymousFreeKey(opencodeGoEffectiveApiKey('', 'public'))).toBe(true);
  });
});

describe('opencode-go free tier locale keys', () => {
  test('provider form hint and models free badge exist in all 4 locales', () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      expect(translations.providersPage.form.opencodeGoPublicFreeHint).toContain('public');
      expect(translations.auth_files.models_free_badge).toBeTruthy();
    }
  });
});
