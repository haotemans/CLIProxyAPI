import { afterEach, describe, expect, test } from 'bun:test';
import { clineToResource } from '../src/features/providers/adapters';
import { PROVIDER_BRAND_ORDER, PROVIDER_DESCRIPTORS } from '../src/features/providers/descriptors';
import { MODEL_DISCOVERY_BRANDS } from '../src/features/providers/sheets/forms/useModelDiscovery';
import { buildProviderGroups } from '../src/features/providers/useProviderWorkbench';
import { apiClient } from '../src/services/api/client';
import { normalizeClineModelPayload } from '../src/services/api/models';
import { providersApi } from '../src/services/api/providers';
import { normalizeConfigResponse } from '../src/services/api/transformers';

const originalGet = apiClient.get;
const originalPut = apiClient.put;
const originalDelete = apiClient.delete;

afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
  apiClient.delete = originalDelete;
});

describe('Cline API key provider', () => {
  test('normalizes the backend contract and exposes a dedicated workbench resource', () => {
    const config = normalizeConfigResponse({
      'cline-api-key': [
        {
          'api-key': 'cline-secret',
          priority: 7,
          weight: 3,
          prefix: 'c',
          'base-url': 'https://api.cline.bot/api/v1',
          'proxy-url': 'socks5://proxy.example:1080',
          headers: { 'X-Custom': 'value' },
          models: [{ name: 'moonshotai/kimi-k3', alias: 'kimi-k3' }],
          'excluded-models': ['anthropic/claude-sonnet-4-6'],
          'disable-cooling': true,
          'auth-index': 'cline:apikey:1',
        },
      ],
    });

    expect(config.clineApiKeys).toEqual([
      {
        apiKey: 'cline-secret',
        priority: 7,
        weight: 3,
        prefix: 'c',
        baseUrl: 'https://api.cline.bot/api/v1',
        proxyUrl: 'socks5://proxy.example:1080',
        headers: { 'X-Custom': 'value' },
        models: [{ name: 'moonshotai/kimi-k3', alias: 'kimi-k3' }],
        excludedModels: ['anthropic/claude-sonnet-4-6'],
        disableCooling: true,
        authIndex: 'cline:apikey:1',
      },
    ]);

    const resource = clineToResource(config.clineApiKeys![0], 0);
    expect(resource.brand).toBe('cline');
    expect(resource.baseUrl).toBe('https://api.cline.bot/api/v1');
    expect(resource.models).toEqual(['moonshotai/kimi-k3']);
    expect(resource.selector).toEqual({
      brand: 'cline',
      apiKey: 'cline-secret',
      baseUrl: 'https://api.cline.bot/api/v1',
      index: 0,
    });
    expect(
      buildProviderGroups(config).find((group) => group.id === 'cline')?.resources
    ).toHaveLength(1);
    // Base URL defaults to the public account API; cloak/fingerprint are Claude-only.
    expect(PROVIDER_DESCRIPTORS.cline.baseUrlRequired).toBe(false);
    expect(PROVIDER_DESCRIPTORS.cline.supportsCloak).toBe(false);
    expect(PROVIDER_DESCRIPTORS.cline.supportsWebsockets).toBe(false);
    expect(PROVIDER_DESCRIPTORS.cline.supportsTestModel).toBe(true);
    expect(PROVIDER_BRAND_ORDER.indexOf('cline')).toBe(
      PROVIDER_BRAND_ORDER.indexOf('opencodeGo') + 1
    );
    expect(MODEL_DISCOVERY_BRANDS).toContain('cline');
  });

  test('lists Cline keys through the dedicated management endpoint', async () => {
    apiClient.get = (async (url: string) => {
      expect(url).toBe('/cline-api-key');
      return {
        'cline-api-key': [
          {
            'api-key': 'cline-key',
            'base-url': 'https://api.cline.bot/api/v1',
            'auth-index': 'cline:apikey:0',
          },
        ],
      };
    }) as typeof apiClient.get;

    await expect(providersApi.getClineConfigs()).resolves.toEqual([
      {
        apiKey: 'cline-key',
        baseUrl: 'https://api.cline.bot/api/v1',
        authIndex: 'cline:apikey:0',
      },
    ]);
  });

  test('creates, updates, and deletes keys while preserving unknown backend fields', async () => {
    const calls: Array<{ method: string; url: string; data?: unknown }> = [];
    let configResponse: unknown = {
      'cline-api-key': [
        {
          'api-key': 'existing',
          'base-url': 'https://api.cline.bot/api/v1',
          'request-retry': 2,
          'future-field': 'preserved',
          'auth-index': 'response-only',
        },
      ],
    };
    apiClient.get = (async (url: string) => {
      calls.push({ method: 'GET', url });
      return configResponse;
    }) as typeof apiClient.get;
    apiClient.put = (async (url: string, data?: unknown) => {
      calls.push({ method: 'PUT', url, data });
      configResponse = { 'cline-api-key': data };
      return undefined;
    }) as typeof apiClient.put;
    apiClient.delete = (async (url: string) => {
      calls.push({ method: 'DELETE', url });
      return undefined;
    }) as typeof apiClient.delete;

    await providersApi.createClineConfig({
      apiKey: 'cline-new',
      weight: 4,
      prefix: 'c',
      baseUrl: 'https://api.cline.bot/api/v1',
      proxyUrl: 'direct',
      models: [{ name: 'moonshotai/kimi-k3', alias: 'kimi-k3' }],
      excludedModels: ['anthropic/claude-sonnet-4-6'],
      disableCooling: true,
    });
    await providersApi.updateClineConfig('existing', 'https://api.cline.bot/api/v1', {
      apiKey: 'existing',
      priority: 9,
      baseUrl: 'https://api.cline.bot/api/v1',
      models: [{ name: 'moonshotai/kimi-k3' }],
    });
    await providersApi.deleteClineConfig('existing', 'https://api.cline.bot/api/v1');

    expect(calls[1]).toEqual({
      method: 'PUT',
      url: '/cline-api-key',
      data: [
        {
          'api-key': 'existing',
          'base-url': 'https://api.cline.bot/api/v1',
          'request-retry': 2,
          'future-field': 'preserved',
          'auth-index': 'response-only',
        },
        {
          'api-key': 'cline-new',
          weight: 4,
          prefix: 'c',
          'base-url': 'https://api.cline.bot/api/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'moonshotai/kimi-k3', alias: 'kimi-k3' }],
          'excluded-models': ['anthropic/claude-sonnet-4-6'],
        },
      ],
    });
    expect(calls[3]).toEqual({
      method: 'PUT',
      url: '/cline-api-key',
      data: [
        {
          'request-retry': 2,
          'future-field': 'preserved',
          'api-key': 'existing',
          priority: 9,
          'base-url': 'https://api.cline.bot/api/v1',
          models: [{ name: 'moonshotai/kimi-k3' }],
        },
        {
          'api-key': 'cline-new',
          weight: 4,
          prefix: 'c',
          'base-url': 'https://api.cline.bot/api/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'moonshotai/kimi-k3', alias: 'kimi-k3' }],
          'excluded-models': ['anthropic/claude-sonnet-4-6'],
        },
      ],
    });
    expect(calls[4]).toEqual({
      method: 'DELETE',
      url: '/cline-api-key?api-key=existing&base-url=https%3A%2F%2Fapi.cline.bot%2Fapi%2Fv1',
    });
  });

  test('never serializes Claude-only cloak or fingerprint fields', async () => {
    const payloads: unknown[] = [];
    apiClient.get = (async () => ({ 'cline-api-key': [] })) as typeof apiClient.get;
    apiClient.put = (async (url: string, data?: unknown) => {
      expect(url).toBe('/cline-api-key');
      payloads.push(data);
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.createClineConfig({
      apiKey: 'cline-new',
      baseUrl: 'https://api.cline.bot/api/v1',
    });

    expect(JSON.stringify(payloads[0])).not.toContain('cloak');
    expect(JSON.stringify(payloads[0])).not.toContain('fingerprint');
  });

  test('discovery payload parser merges buckets with dedupe and envelope unwrap', () => {
    // Bucketed object: arrays merged, deduped by id.
    const bucketed = {
      recommended: [
        { id: 'moonshotai/kimi-k3', name: 'kimi-k3', description: 'flagship' },
        { id: 'anthropic/claude-sonnet-4-6', name: 'Claude Sonnet' },
      ],
      free: [{ id: 'moonshotai/kimi-k3', name: 'kimi-k3' }],
    };
    const merged = normalizeClineModelPayload(bucketed, { dedupe: true });
    expect(merged.map((m) => m.name)).toEqual([
      'moonshotai/kimi-k3',
      'anthropic/claude-sonnet-4-6',
    ]);

    // Envelope unwrap.
    const envelope = { data: [{ id: 'deepseek/deepseek-v4.1-flash' }] };
    expect(normalizeClineModelPayload(envelope).map((m) => m?.name ?? '')).toEqual([
      'deepseek/deepseek-v4.1-flash',
    ]);

    // Bare array passthrough.
    const bare = [{ id: 'glm-5.3', name: 'GLM' }];
    expect(normalizeClineModelPayload(bare).map((m) => m?.name ?? '')).toEqual(['glm-5.3']);
  });
});
