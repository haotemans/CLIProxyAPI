import { afterEach, describe, expect, test } from 'bun:test';
import { opencodeGoToResource } from '../src/features/providers/adapters';
import { PROVIDER_BRAND_ORDER, PROVIDER_DESCRIPTORS } from '../src/features/providers/descriptors';
import { MODEL_DISCOVERY_BRANDS } from '../src/features/providers/sheets/forms/useModelDiscovery';
import { buildProviderGroups } from '../src/features/providers/useProviderWorkbench';
import { apiClient } from '../src/services/api/client';
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

describe('OpenCode Go API key provider', () => {
  test('normalizes the backend contract and exposes a dedicated workbench resource', () => {
    const config = normalizeConfigResponse({
      'opencode-go-api-key': [
        {
          'api-key': 'opencode-go-secret',
          priority: 7,
          weight: 3,
          prefix: 'ocg',
          'base-url': 'https://opencode.ai/zen/go/v1',
          'proxy-url': 'socks5://proxy.example:1080',
          headers: { 'X-Custom': 'value' },
          models: [{ name: 'kimi-k3', alias: 'k3' }],
          'excluded-models': ['grok-code-fast-1'],
          'disable-cooling': true,
          'auth-index': 'opencode-go:apikey:1',
        },
      ],
    });

    expect(config.opencodeGoApiKeys).toEqual([
      {
        apiKey: 'opencode-go-secret',
        priority: 7,
        weight: 3,
        prefix: 'ocg',
        baseUrl: 'https://opencode.ai/zen/go/v1',
        proxyUrl: 'socks5://proxy.example:1080',
        headers: { 'X-Custom': 'value' },
        models: [{ name: 'kimi-k3', alias: 'k3' }],
        excludedModels: ['grok-code-fast-1'],
        disableCooling: true,
        authIndex: 'opencode-go:apikey:1',
      },
    ]);

    const resource = opencodeGoToResource(config.opencodeGoApiKeys![0], 0);
    expect(resource.brand).toBe('opencodeGo');
    expect(resource.baseUrl).toBe('https://opencode.ai/zen/go/v1');
    expect(resource.models).toEqual(['kimi-k3']);
    expect(resource.selector).toEqual({
      brand: 'opencodeGo',
      apiKey: 'opencode-go-secret',
      baseUrl: 'https://opencode.ai/zen/go/v1',
      index: 0,
    });
    expect(
      buildProviderGroups(config).find((group) => group.id === 'opencodeGo')?.resources
    ).toHaveLength(1);
    // Base URL defaults to the public endpoint; cloak/fingerprint are Claude-only.
    expect(PROVIDER_DESCRIPTORS.opencodeGo.baseUrlRequired).toBe(false);
    expect(PROVIDER_DESCRIPTORS.opencodeGo.supportsCloak).toBe(false);
    expect(PROVIDER_DESCRIPTORS.opencodeGo.supportsWebsockets).toBe(false);
    expect(PROVIDER_DESCRIPTORS.opencodeGo.supportsTestModel).toBe(true);
    expect(PROVIDER_BRAND_ORDER.indexOf('opencodeGo')).toBe(
      PROVIDER_BRAND_ORDER.indexOf('commandcode') + 1
    );
    expect(MODEL_DISCOVERY_BRANDS).toContain('opencodeGo');
  });

  test('lists OpenCode Go keys through the dedicated management endpoint', async () => {
    apiClient.get = (async (url: string) => {
      expect(url).toBe('/opencode-go-api-key');
      return {
        'opencode-go-api-key': [
          {
            'api-key': 'opencode-go-key',
            'base-url': 'https://opencode.ai/zen/go/v1',
            'auth-index': 'opencode-go:apikey:0',
          },
        ],
      };
    }) as typeof apiClient.get;

    await expect(providersApi.getOpencodeGoConfigs()).resolves.toEqual([
      {
        apiKey: 'opencode-go-key',
        baseUrl: 'https://opencode.ai/zen/go/v1',
        authIndex: 'opencode-go:apikey:0',
      },
    ]);
  });

  test('creates, updates, and deletes keys while preserving unknown backend fields', async () => {
    const calls: Array<{ method: string; url: string; data?: unknown }> = [];
    let configResponse: unknown = {
      'opencode-go-api-key': [
        {
          'api-key': 'existing',
          'base-url': 'https://opencode.ai/zen/go/v1',
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
      configResponse = { 'opencode-go-api-key': data };
      return undefined;
    }) as typeof apiClient.put;
    apiClient.delete = (async (url: string) => {
      calls.push({ method: 'DELETE', url });
      return undefined;
    }) as typeof apiClient.delete;

    await providersApi.createOpencodeGoConfig({
      apiKey: 'opencode-go-new',
      weight: 4,
      prefix: 'ocg',
      baseUrl: 'https://opencode.ai/zen/go/v1',
      proxyUrl: 'direct',
      models: [{ name: 'kimi-k3', alias: 'k3' }],
      excludedModels: ['grok-code-fast-1'],
      disableCooling: true,
    });
    await providersApi.updateOpencodeGoConfig('existing', 'https://opencode.ai/zen/go/v1', {
      apiKey: 'existing',
      priority: 9,
      baseUrl: 'https://opencode.ai/zen/go/v1',
      models: [{ name: 'kimi-k3' }],
    });
    await providersApi.deleteOpencodeGoConfig('existing', 'https://opencode.ai/zen/go/v1');

    expect(calls[1]).toEqual({
      method: 'PUT',
      url: '/opencode-go-api-key',
      data: [
        {
          'api-key': 'existing',
          'base-url': 'https://opencode.ai/zen/go/v1',
          'request-retry': 2,
          'future-field': 'preserved',
          'auth-index': 'response-only',
        },
        {
          'api-key': 'opencode-go-new',
          weight: 4,
          prefix: 'ocg',
          'base-url': 'https://opencode.ai/zen/go/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'kimi-k3', alias: 'k3' }],
          'excluded-models': ['grok-code-fast-1'],
        },
      ],
    });
    expect(calls[3]).toEqual({
      method: 'PUT',
      url: '/opencode-go-api-key',
      data: [
        {
          'request-retry': 2,
          'future-field': 'preserved',
          'api-key': 'existing',
          priority: 9,
          'base-url': 'https://opencode.ai/zen/go/v1',
          models: [{ name: 'kimi-k3' }],
        },
        {
          'api-key': 'opencode-go-new',
          weight: 4,
          prefix: 'ocg',
          'base-url': 'https://opencode.ai/zen/go/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'kimi-k3', alias: 'k3' }],
          'excluded-models': ['grok-code-fast-1'],
        },
      ],
    });
    expect(calls[4]).toEqual({
      method: 'DELETE',
      url: '/opencode-go-api-key?api-key=existing&base-url=https%3A%2F%2Fopencode.ai%2Fzen%2Fgo%2Fv1',
    });
  });

  test('never serializes Claude-only cloak or fingerprint fields', async () => {
    const payloads: unknown[] = [];
    apiClient.get = (async () => ({ 'opencode-go-api-key': [] })) as typeof apiClient.get;
    apiClient.put = (async (url: string, data?: unknown) => {
      expect(url).toBe('/opencode-go-api-key');
      payloads.push(data);
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.createOpencodeGoConfig({
      apiKey: 'opencode-go-new',
      baseUrl: 'https://opencode.ai/zen/go/v1',
    });

    expect(JSON.stringify(payloads[0])).not.toContain('cloak');
    expect(JSON.stringify(payloads[0])).not.toContain('fingerprint');
  });
});
