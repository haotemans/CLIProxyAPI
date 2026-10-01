import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  buildPluginOAuthProviderCards,
  PLUGIN_OAUTH_BRAND_OVERRIDES,
  resolveAuthStartUrl,
} from '@/pages/oauthPluginProviders';
import { oauthApi, pluginsApi, type BuiltInOAuthProvider } from '@/services/api';
import { apiClient } from '@/services/api/client';
import type { PluginListEntry } from '@/types';

const BUILTIN_IDS = new Set<string>([
  'meta',
  'kimi',
  'kimi-ai',
  'codex',
  'anthropic',
  'antigravity',
  'xai',
  'devin',
  'cline',
  'cursor',
  'kiro',
]);

const pluginEntry = (overrides: Partial<PluginListEntry>): PluginListEntry =>
  ({
    id: 'mirasim',
    name: 'mirasim',
    registered: true,
    effectiveEnabled: true,
    supportsOAuth: true,
    oauthProvider: 'mirasim',
    logo: '',
    metadata: null,
    ...overrides,
  }) as PluginListEntry;

describe('plugin OAuth provider cards', () => {
  test('merges eligible plugin providers into cards', () => {
    const cards = buildPluginOAuthProviderCards(
      [pluginEntry({ metadata: { name: 'Mirasim' } as PluginListEntry['metadata'], logo: 'mirasim.svg' })],
      'http://127.0.0.1:8317',
      BUILTIN_IDS
    );
    expect(cards).toHaveLength(1);
    expect(cards[0]).toMatchObject({ kind: 'plugin', id: 'mirasim', title: 'Mirasim' });
    expect(cards[0].icon).toBeTruthy();
  });

  test('skips unregistered, disabled, unsupported and provider-less entries', () => {
    const cards = buildPluginOAuthProviderCards(
      [
        pluginEntry({ id: 'a', oauthProvider: 'a', registered: false }),
        pluginEntry({ id: 'b', oauthProvider: 'b', effectiveEnabled: false }),
        pluginEntry({ id: 'c', oauthProvider: 'c', supportsOAuth: false }),
        pluginEntry({ id: 'd', oauthProvider: undefined } as Partial<PluginListEntry>),
      ],
      '',
      BUILTIN_IDS
    );
    expect(cards).toHaveLength(0);
  });

  test('dedupes providers against builtin ids and across plugins', () => {
    const cards = buildPluginOAuthProviderCards(
      [
        pluginEntry({ id: 'builtin-collider', oauthProvider: 'cursor', metadata: null }),
        pluginEntry({ id: 'first', oauthProvider: 'mirasim', metadata: { name: 'First' } as PluginListEntry['metadata'] }),
        pluginEntry({ id: 'second', oauthProvider: 'mirasim', metadata: { name: 'Second' } as PluginListEntry['metadata'] }),
      ],
      '',
      BUILTIN_IDS
    );
    expect(cards).toHaveLength(1);
    expect(cards[0].title).toBe('First');
  });

  test('uses title fallback to plugin id', () => {
    const cards = buildPluginOAuthProviderCards(
      [pluginEntry({ id: 'mirasim', metadata: null })],
      '',
      BUILTIN_IDS
    );
    expect(cards).toHaveLength(1);
    expect(cards[0].title).toBe('mirasim');
  });

  test('mirasim is a builtin OAuth card (icon, title key); plugin override is empty', () => {
    // Mirasim moved from the dynamic plugin merge into the static PROVIDERS
    // list, so it no longer needs a brand override entry.
    expect(Object.keys(PLUGIN_OAUTH_BRAND_OVERRIDES)).toHaveLength(0);
    const source = readFileSync('src/pages/OAuthPage.tsx', 'utf8');
    expect(source).toContain("id: 'mirasim'");
    expect(source).toContain('iconMirasim');
    expect(readFileSync('src/assets/icons/mirasim.svg', 'utf8')).toContain('<svg');
  });

  test('unknown plugin providers keep the generic rendering', () => {
    const cards = buildPluginOAuthProviderCards(
      [pluginEntry({ oauthProvider: 'custom-sso' })],
      '',
      BUILTIN_IDS
    );
    expect(cards).toHaveLength(1);
    expect(cards[0].titleKey).toBeUndefined();
    expect(cards[0].hintKey).toBeUndefined();
  });

  test('overrides are presentation-only: dedupe still uses oauth_provider', () => {
    const cards = buildPluginOAuthProviderCards(
      [
        pluginEntry({ id: 'first', oauthProvider: 'mirasim', metadata: null }),
        pluginEntry({ id: 'second', oauthProvider: 'mirasim', metadata: null }),
      ],
      '',
      BUILTIN_IDS
    );
    expect(cards).toHaveLength(1);
    expect(cards[0].id).toBe('mirasim');
  });

  test('mirasim override locale keys exist in every locale', () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      expect(translations.auth_login.mirasim_oauth_title).toBe('Mirasim OAuth');
      expect(translations.auth_login.mirasim_oauth_hint).toBeTruthy();
    }
  });

  test('OAuth page merges the lists with static cards first', () => {
    const source = readFileSync('src/pages/OAuthPage.tsx', 'utf8');
    expect(source).toContain('[...PROVIDERS, ...pluginProviders]');
    expect(source).toContain('buildPluginOAuthProviderCards');
    expect(source).toContain('BUILTIN_PROVIDER_IDS');
    // Failure-tolerant path: plugins fetch failure resets to static-only.
    expect(source).toContain('setPluginProviders([])');
  });

  test('/v0/management/plugins fetch failure leaves static cards only', async () => {
    const get = spyOn(apiClient, 'get').mockRejectedValue(new Error('boom'));
    try {
      await expect(pluginsApi.list()).rejects.toThrow('boom');
    } finally {
      get.mockRestore();
    }
  });
});

describe('resolveAuthStartUrl', () => {
  test('resolves root-relative plugin start urls against the origin', () => {
    const relative = '/v0/resource/plugins/mirasim/oauth/start?state=abc-123';
    expect(resolveAuthStartUrl('http://127.0.0.1:8317', relative)).toBe(
      'http://127.0.0.1:8317/v0/resource/plugins/mirasim/oauth/start?state=abc-123'
    );
    expect(resolveAuthStartUrl('https://cpa.example.com', relative)).toBe(
      'https://cpa.example.com/v0/resource/plugins/mirasim/oauth/start?state=abc-123'
    );
  });

  test('passes absolute http(s) URLs through', () => {
    const absolute = 'https://login.example.com/oauth?state=s';
    expect(resolveAuthStartUrl('http://127.0.0.1:8317', absolute)).toBe(absolute);
  });

  test('handles blanks', () => {
    expect(resolveAuthStartUrl('http://localhost:8317', '')).toBe('');
    expect(resolveAuthStartUrl('http://localhost:8317', '   ')).toBe('');
  });

  test('OAuth page applies resolution in startAuth', () => {
    const source = readFileSync('src/pages/OAuthPage.tsx', 'utf8');
    expect(source).toContain('resolveAuthStartUrl(window.location.origin, res.url)');
  });
});

describe('plugin OAuth flow parity', () => {
  test('plugin provider ids route through the same auth-url endpoint', async () => {
    const provider = 'mirasim' as BuiltInOAuthProvider;
    const response = {
      status: 'ok',
      url: '/v0/resource/plugins/mirasim/oauth/start?state=mira-1',
      state: 'mira-1',
    };
    const signal = new AbortController().signal;
    const get = spyOn(apiClient, 'get').mockResolvedValue(response);
    try {
      expect(await oauthApi.startAuth(provider, signal)).toEqual(response);
      expect(get).toHaveBeenLastCalledWith('/mirasim-auth-url', { params: undefined, signal });
    } finally {
      get.mockRestore();
    }
  });

  test('plugin i18n keys exist in every locale', () => {
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
        expect(translations.auth_login[`plugin_${suffix}`], `${locale} plugin_${suffix}`).toBeTruthy();
      }
    }
  });
});
