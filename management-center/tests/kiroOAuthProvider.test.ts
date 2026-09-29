import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { getAuthFileIcon } from '@/features/authFiles/constants';
import { providerLabel } from '@/features/dashboard/utils';
import { apiClient } from '@/services/api/client';
import { oauthApi, type BuiltInOAuthProvider } from '@/services/api/oauth';
import { normalizeOAuthProviderKey } from '@/utils/providerKeys';

describe('Kiro provider and device OAuth', () => {
  test('uses the backend kiro device endpoint without a webui callback param', async () => {
    const provider: BuiltInOAuthProvider = 'kiro';
    const response = {
      status: 'ok',
      url: 'https://device.sso.us-east-1.amazonaws.com/',
      state: 'kiro-fixture',
      flow: 'device',
      user_code: 'ABCD-EFGH',
      expires_in: 600,
    };
    const signal = new AbortController().signal;
    const get = spyOn(apiClient, 'get').mockResolvedValue(response);
    try {
      expect(await oauthApi.startAuth(provider, signal)).toEqual(response);
      expect(get).toHaveBeenLastCalledWith('/kiro-auth-url', { params: undefined, signal });
      await oauthApi.startAuth('Kiro');
      expect(get).toHaveBeenLastCalledWith('/kiro-auth-url', { params: undefined });
    } finally {
      get.mockRestore();
    }
  });

  test('resolves names and icons for Kiro tokens', () => {
    expect(normalizeOAuthProviderKey(' Kiro ')).toBe('kiro');
    expect(getAuthFileIcon('kiro', 'dark')).toBeTruthy();
    expect(getAuthFileIcon('kiro', 'light')).toBe(getAuthFileIcon('kiro', 'dark'));
    expect(readFileSync('src/assets/icons/kiro.svg', 'utf8')).toContain('<svg');
    expect(providerLabel('kiro', 'Unknown')).toBe('Kiro');
  });

  test('registers the device-flow card without a manual OAuth callback', () => {
    const source = readFileSync('src/pages/OAuthPage.tsx', 'utf8');
    expect(source).toContain("id: 'kiro'");
    expect(source).toContain('auth_login.kiro_oauth_title');
    const callbackProviders = source.match(
      /const CALLBACK_SUPPORTED = new Set<string>\(([^;]+)\);/
    );
    expect(callbackProviders?.[1]).not.toContain("'kiro'");
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
        expect(translations.auth_login[`kiro_${suffix}`]).toBeTruthy();
      }
      expect(translations.auth_files.filter_kiro).toBe('Kiro');
      expect(translations.auth_login.device_code_label).toBeTruthy();
      expect(translations.auth_login.device_code_copy).toBeTruthy();
    }
  });
});
