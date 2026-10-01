import { afterEach, describe, expect, test } from 'bun:test';
import { commandcodeToResource } from '../src/features/providers/adapters';
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

describe('Commandcode API key provider', () => {
  test('normalizes the backend contract and exposes a dedicated workbench resource', () => {
    const config = normalizeConfigResponse({
      'commandcode-api-key': [
        {
          'api-key': 'commandcode-secret',
          priority: 7,
          weight: 3,
          prefix: 'cc',
          'base-url': 'https://api.commandcode.ai/provider/v1',
          'proxy-url': 'socks5://proxy.example:1080',
          headers: { 'X-Custom': 'value' },
          models: [{ name: 'deepseek/deepseek-v4.1-flash', alias: 'deepseek-flash' }],
          'excluded-models': ['z-ai/glm-5.3-flash'],
          'disable-cooling': true,
          'auth-index': 'commandcode:apikey:1',
        },
      ],
    });

    expect(config.commandcodeApiKeys).toEqual([
      {
        apiKey: 'commandcode-secret',
        priority: 7,
        weight: 3,
        prefix: 'cc',
        baseUrl: 'https://api.commandcode.ai/provider/v1',
        proxyUrl: 'socks5://proxy.example:1080',
        headers: { 'X-Custom': 'value' },
        models: [{ name: 'deepseek/deepseek-v4.1-flash', alias: 'deepseek-flash' }],
        excludedModels: ['z-ai/glm-5.3-flash'],
        disableCooling: true,
        authIndex: 'commandcode:apikey:1',
      },
    ]);

    const resource = commandcodeToResource(config.commandcodeApiKeys![0], 0);
    expect(resource.brand).toBe('commandcode');
    expect(resource.baseUrl).toBe('https://api.commandcode.ai/provider/v1');
    expect(resource.models).toEqual(['deepseek/deepseek-v4.1-flash']);
    expect(resource.selector).toEqual({
      brand: 'commandcode',
      apiKey: 'commandcode-secret',
      baseUrl: 'https://api.commandcode.ai/provider/v1',
      index: 0,
    });
    expect(
      buildProviderGroups(config).find((group) => group.id === 'commandcode')?.resources
    ).toHaveLength(1);
    // Base URL defaults to the public endpoint; cloak/fingerprint are Claude-only.
    expect(PROVIDER_DESCRIPTORS.commandcode.baseUrlRequired).toBe(false);
    expect(PROVIDER_DESCRIPTORS.commandcode.supportsCloak).toBe(false);
    expect(PROVIDER_DESCRIPTORS.commandcode.supportsWebsockets).toBe(false);
    expect(PROVIDER_DESCRIPTORS.commandcode.supportsTestModel).toBe(true);
    expect(PROVIDER_BRAND_ORDER.indexOf('commandcode')).toBe(
      PROVIDER_BRAND_ORDER.indexOf('mirasim') + 1
    );
    expect(MODEL_DISCOVERY_BRANDS).toContain('commandcode');
  });

  test('lists Commandcode keys through the dedicated management endpoint', async () => {
    apiClient.get = (async (url: string) => {
      expect(url).toBe('/commandcode-api-key');
      return {
        'commandcode-api-key': [
          {
            'api-key': 'commandcode-key',
            'base-url': 'https://api.commandcode.ai/provider/v1',
            'auth-index': 'commandcode:apikey:0',
          },
        ],
      };
    }) as typeof apiClient.get;

    await expect(providersApi.getCommandcodeConfigs()).resolves.toEqual([
      {
        apiKey: 'commandcode-key',
        baseUrl: 'https://api.commandcode.ai/provider/v1',
        authIndex: 'commandcode:apikey:0',
      },
    ]);
  });

  test('creates, updates, and deletes keys while preserving unknown backend fields', async () => {
    const calls: Array<{ method: string; url: string; data?: unknown }> = [];
    let configResponse: unknown = {
      'commandcode-api-key': [
        {
          'api-key': 'existing',
          'base-url': 'https://api.commandcode.ai/provider/v1',
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
      configResponse = { 'commandcode-api-key': data };
      return undefined;
    }) as typeof apiClient.put;
    apiClient.delete = (async (url: string) => {
      calls.push({ method: 'DELETE', url });
      return undefined;
    }) as typeof apiClient.delete;

    await providersApi.createCommandcodeConfig({
      apiKey: 'commandcode-new',
      weight: 4,
      prefix: 'cc',
      baseUrl: 'https://api.commandcode.ai/provider/v1',
      proxyUrl: 'direct',
      models: [{ name: 'deepseek/deepseek-v4.1-flash', alias: 'deepseek-flash' }],
      excludedModels: ['z-ai/glm-5.3-flash'],
      disableCooling: true,
    });
    await providersApi.updateCommandcodeConfig('existing', 'https://api.commandcode.ai/provider/v1', {
      apiKey: 'existing',
      priority: 9,
      baseUrl: 'https://api.commandcode.ai/provider/v1',
      models: [{ name: 'deepseek/deepseek-v4.1-flash' }],
    });
    await providersApi.deleteCommandcodeConfig('existing', 'https://api.commandcode.ai/provider/v1');

    expect(calls[1]).toEqual({
      method: 'PUT',
      url: '/commandcode-api-key',
      data: [
        {
          'api-key': 'existing',
          'base-url': 'https://api.commandcode.ai/provider/v1',
          'request-retry': 2,
          'future-field': 'preserved',
          'auth-index': 'response-only',
        },
        {
          'api-key': 'commandcode-new',
          weight: 4,
          prefix: 'cc',
          'base-url': 'https://api.commandcode.ai/provider/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'deepseek/deepseek-v4.1-flash', alias: 'deepseek-flash' }],
          'excluded-models': ['z-ai/glm-5.3-flash'],
        },
      ],
    });
    expect(calls[3]).toEqual({
      method: 'PUT',
      url: '/commandcode-api-key',
      data: [
        {
          'request-retry': 2,
          'future-field': 'preserved',
          'api-key': 'existing',
          priority: 9,
          'base-url': 'https://api.commandcode.ai/provider/v1',
          models: [{ name: 'deepseek/deepseek-v4.1-flash' }],
        },
        {
          'api-key': 'commandcode-new',
          weight: 4,
          prefix: 'cc',
          'base-url': 'https://api.commandcode.ai/provider/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'deepseek/deepseek-v4.1-flash', alias: 'deepseek-flash' }],
          'excluded-models': ['z-ai/glm-5.3-flash'],
        },
      ],
    });
    expect(calls[4]).toEqual({
      method: 'DELETE',
      url: '/commandcode-api-key?api-key=existing&base-url=https%3A%2F%2Fapi.commandcode.ai%2Fprovider%2Fv1',
    });
  });

  test('never serializes Claude-only cloak or fingerprint fields', async () => {
    const payloads: unknown[] = [];
    apiClient.get = (async () => ({ 'commandcode-api-key': [] })) as typeof apiClient.get;
    apiClient.put = (async (url: string, data?: unknown) => {
      expect(url).toBe('/commandcode-api-key');
      payloads.push(data);
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.createCommandcodeConfig({
      apiKey: 'commandcode-new',
      baseUrl: 'https://api.commandcode.ai/provider/v1',
    });

    expect(JSON.stringify(payloads[0])).not.toContain('cloak');
    expect(JSON.stringify(payloads[0])).not.toContain('fingerprint');
  });
});
