import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { getAuthFileIcon } from '@/features/authFiles/constants';
import { providerLabel } from '@/features/dashboard/utils';
import { apiClient } from '@/services/api/client';
import { oauthApi, type BuiltInOAuthProvider } from '@/services/api/oauth';
import { normalizeOAuthProviderKey } from '@/utils/providerKeys';

describe('Cursor provider and poll OAuth', () => {
  test('uses the backend cursor auth-url endpoint without a webui callback param', async () => {
    const provider: BuiltInOAuthProvider = 'cursor';
    const response = {
      status: 'ok',
      url: 'https://cursor.com/loginDeepControl?challenge=abc&uuid=123',
      state: 'cur-fixture',
      flow: 'poll',
    };
    const signal = new AbortController().signal;
    const get = spyOn(apiClient, 'get').mockResolvedValue(response);
    try {
      expect(await oauthApi.startAuth(provider, signal)).toEqual(response);
      expect(get).toHaveBeenLastCalledWith('/cursor-auth-url', { params: undefined, signal });
      await oauthApi.startAuth('Cursor');
      expect(get).toHaveBeenLastCalledWith('/cursor-auth-url', { params: undefined });
    } finally {
      get.mockRestore();
    }
  });

  test('resolves names and icons for Cursor tokens', () => {
    expect(normalizeOAuthProviderKey(' Cursor ')).toBe('cursor');
    expect(getAuthFileIcon('cursor', 'dark')).toBeTruthy();
    expect(getAuthFileIcon('cursor', 'light')).toBe(getAuthFileIcon('cursor', 'dark'));
    expect(readFileSync('src/assets/icons/cursor.svg', 'utf8')).toContain('<svg');
    expect(providerLabel('cursor', 'Unknown')).toBe('Cursor');
  });

  test('registers the poll-flow card without a manual OAuth callback', () => {
    const source = readFileSync('src/pages/OAuthPage.tsx', 'utf8');
    expect(source).toContain("id: 'cursor'");
    expect(source).toContain('auth_login.cursor_oauth_title');
    const callbackProviders = source.match(
      /const CALLBACK_SUPPORTED = new Set<string>\(([^;]+)\);/
    );
    expect(callbackProviders?.[1]).not.toContain("'cursor'");
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      for (const suffix of [
        'oauth_title',
        'oauth_button',
        'oauth_hint',
        'oauth_url_label',
        'open_link',
        'copy_link',
        'oauth_status_waiting',
        'oauth_status_success',
        'oauth_status_error',
        'oauth_start_error',
        'oauth_polling_error',
      ]) {
        expect(translations.auth_login[`cursor_${suffix}`]).toBeTruthy();
      }
      expect(translations.auth_files.filter_cursor).toBe('Cursor');
    }
  });
});
