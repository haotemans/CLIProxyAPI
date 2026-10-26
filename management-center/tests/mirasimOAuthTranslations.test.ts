import { describe, expect, test } from 'bun:test';
import en from '../src/i18n/locales/en.json';
import zhCN from '../src/i18n/locales/zh-CN.json';
import zhTW from '../src/i18n/locales/zh-TW.json';
import ru from '../src/i18n/locales/ru.json';

describe('Mirasim OAuth translations', () => {
  for (const [locale, messages] of Object.entries({ en, 'zh-CN': zhCN, 'zh-TW': zhTW, ru })) {
    test(`${locale} translates every OAuth card action and status`, () => {
      const login = messages.auth_login as Record<string, string>;
      // OAuthPage builds these keys dynamically for each built-in provider.
      const suffixes = Object.keys(en.auth_login)
        .filter((key) => key.startsWith('cursor_'))
        .map((key) => key.slice('cursor_'.length));
      for (const suffix of suffixes) {
        const value = login[`mirasim_${suffix}`];
        expect(value).toBeString();
        expect(value?.trim()).not.toBe('');
        expect(value).not.toContain('auth_login.');
      }
    });
  }
});
