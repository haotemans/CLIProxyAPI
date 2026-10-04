import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { distributionKeysApi } from '@/features/distribution/api';
import {
  distributionFormFromRow,
  distributionKeyStatus,
  distributionWritePayload,
  emptyDistributionForm,
  hasQuota,
  localInputToRfc3339,
  parseModelListText,
  quotaUsageRatio,
  validateDistributionForm,
} from '@/features/distribution/logic';
import { apiClient } from '@/services/api/client';

describe('distribution key logic', () => {
  test('status precedence: expired > disabled > active', () => {
    expect(distributionKeyStatus({ enabled: true, expired: true })).toBe('expired');
    expect(distributionKeyStatus({ enabled: false, expired: true })).toBe('expired');
    expect(distributionKeyStatus({ enabled: false })).toBe('disabled');
    expect(distributionKeyStatus({ enabled: true })).toBe('active');
  });

  test('quota ratio clamps and unlimited stays null', () => {
    expect(quotaUsageRatio(10, 2.5)).toBeCloseTo(0.25);
    expect(quotaUsageRatio(10, 99)).toBe(1);
    expect(quotaUsageRatio(10, -1)).toBe(0);
    expect(quotaUsageRatio(0, 5)).toBeNull();
    expect(quotaUsageRatio(undefined, 5)).toBeNull();
    expect(hasQuota(0)).toBe(false);
    expect(hasQuota(5)).toBe(true);
  });

  test('model list parsing dedupes and trims (lines and commas)', () => {
    expect(parseModelListText('gpt-5\n claude-sonnet-4-5 \n\ngpt-5,')).toEqual([
      'gpt-5',
      'claude-sonnet-4-5',
    ]);
    expect(parseModelListText('')).toEqual([]);
  });

  test('form validation catches bad quota and expiry', () => {
    expect(validateDistributionForm({ ...emptyDistributionForm, quotaUsdText: '-2' }, false)).toEqual({
      field: 'quota',
    });
    expect(validateDistributionForm({ ...emptyDistributionForm, expiresAtLocal: 'bogus' }, false)).toEqual({
      field: 'expiresAt',
    });
    expect(validateDistributionForm(emptyDistributionForm, true)).toBeNull();
  });

  test('write payload: empty models = all, empty quota = 0, key only on create', () => {
    const values = {
      ...emptyDistributionForm,
      key: ' dk-custom ',
      name: 'alice',
      allowedModelsText: 'gpt-5\ngpt-5',
      quotaUsdText: '',
    };
    const createPayload = distributionWritePayload(values, true);
    expect(createPayload.key).toBe('dk-custom');
    expect(createPayload.allowed_models).toEqual(['gpt-5']);
    expect(createPayload.quota_usd).toBe(0);
    expect(createPayload.enabled).toBe(true);
    const editPayload = distributionWritePayload(values, false);
    expect(editPayload.key).toBeUndefined();
  });

  test('edit form round-trips RFC3339 into datetime-local once', () => {
    const from = distributionFormFromRow({
      key_hash: 'abcd1234',
      key_masked: 'dk-ab…1234',
      name: 'alice',
      enabled: false,
      expires_at: '2027-01-01T00:00:00Z',
      allowed_models: ['gpt-5'],
      quota_usd: 3,
      spent_usd: 0,
      requests: 0,
    });
    expect(from.name).toBe('alice');
    expect(from.enabled).toBe(false);
    expect(from.allowedModelsText).toBe('gpt-5');
    expect(from.quotaUsdText).toBe('3');
    // Expiry converts to local input and back to a valid RFC3339 timestamp.
    const iso = localInputToRfc3339(from.expiresAtLocal);
    expect(iso.endsWith('Z')).toBe(true);
    expect(Date.parse(iso)).toBe(Date.parse('2027-01-01T00:00:00Z'));
  });
});

describe('distribution keys api', () => {
  test('create posts body, list calls GET, update/delete/reset target key-hash', async () => {
    const calls: Array<{ method: string; url: string; data?: unknown }> = [];
    const post = spyOn(apiClient, 'post').mockImplementation(async (url: string, data?: unknown) => {
      calls.push({ method: 'POST', url, data });
      return { key: 'dk-x' } as never;
    });
    const get = spyOn(apiClient, 'get').mockImplementation(async (url: string) => {
      calls.push({ method: 'GET', url });
      return { 'distribution-keys': [] } as never;
    });
    const put = spyOn(apiClient, 'put').mockImplementation(async (url: string, data?: unknown) => {
      calls.push({ method: 'PUT', url, data });
      return {} as never;
    });
    const del = spyOn(apiClient, 'delete').mockImplementation(async (url: string) => {
      calls.push({ method: 'DELETE', url });
      return {} as never;
    });
    try {
      await distributionKeysApi.list();
      await distributionKeysApi.create({ name: 'a', enabled: true });
      await distributionKeysApi.update('hash1', { enabled: false });
      await distributionKeysApi.remove('hash1');
      await distributionKeysApi.resetUsage('hash1');
      await distributionKeysApi.usage();
    } finally {
      post.mockRestore();
      get.mockRestore();
      put.mockRestore();
      del.mockRestore();
    }
    expect(calls[0]).toEqual({ method: 'GET', url: '/distribution-keys' });
    expect(calls[1]).toEqual({ method: 'POST', url: '/distribution-keys', data: { name: 'a', enabled: true } });
    expect(calls[2]).toEqual({ method: 'PUT', url: '/distribution-keys?key-hash=hash1', data: { enabled: false } });
    expect(calls[3]).toEqual({ method: 'DELETE', url: '/distribution-keys?key-hash=hash1' });
    expect(calls[4]).toEqual({ method: 'POST', url: '/distribution-keys/reset-usage', data: { key_hash: 'hash1' } });
    expect(calls[5]).toEqual({ method: 'GET', url: '/distribution-keys/usage' });
  });
});

describe('distribution locales', () => {
  test('all 4 locales carry nav entries and the full section', () => {
    const required = [
      'title', 'create', 'save', 'edit', 'delete', 'reset_usage', 'status_active',
      'status_disabled', 'status_expired', 'quota_progress', 'quota_unlimited',
      'models_all', 'models_limited', 'expiry_never', 'expiry_at', 'window_stats',
      'form_key', 'form_name', 'form_enabled', 'form_expiry', 'form_models',
      'form_quota', 'form_error_expiry', 'form_error_quota', 'created_title',
      'created_hint', 'copy_key', 'empty_title', 'empty_desc', 'updated',
      'deleted', 'usage_reset', 'last_request', 'no_traffic', 'subtitle',
      'create_title', 'edit_title',
    ];
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const doc = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      expect(doc.nav.distribution).toBeTruthy();
      expect(doc.nav_meta.distribution).toBeTruthy();
      for (const key of required) {
        expect(String(doc.distribution[key] ?? ''), `${locale}.${key}`).not.toBe('');
      }
      expect(doc.distribution.quota_progress).toContain('{{spent}}');
      expect(doc.distribution.quota_progress).toContain('{{quota}}');
    }
  });
});
