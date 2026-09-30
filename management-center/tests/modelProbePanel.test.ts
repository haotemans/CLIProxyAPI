import { describe, expect, spyOn, test } from 'bun:test';
import { modelProbeApi } from '@/features/authFiles/modelProbe/api';
import {
  formatCheckedAt,
  mergeProbeSummary,
  PROBE_SUPPORTED_PROVIDERS,
  rowsToMap,
} from '@/features/authFiles/modelProbe/logic';
import { apiClient } from '@/services/api/client';

describe('rowsToMap', () => {
  test('keys rows by auth_file, skips blank names', () => {
    const map = rowsToMap([
      { auth_file: 'cursor-a.json', probed: true, usable: 1, pruned: 2 },
      { auth_file: '', probed: false },
      { auth_file: '  kiro.json  ', probed: false } as never,
    ]);
    expect(Object.keys(map)).toContain('cursor-a.json');
    expect(Object.keys(map)).toContain('kiro.json');
    expect(Object.keys(map).length).toBe(2);
  });
});

describe('mergeProbeSummary', () => {
  test('writes summary for a new row', () => {
    const result = mergeProbeSummary({}, 'a.json', {
      probed: true,
      checked_at: '2026-10-01T00:00:00Z',
      usable: 3,
      pruned: 1,
    });
    expect(result['a.json'].probed).toBe(true);
    expect(result['a.json'].usable).toBe(3);
    expect(result['a.json'].checked_at).toBe('2026-10-01T00:00:00Z');
  });

  test('merges over an existing row, preserving provider data', () => {
    const prior = { 'a.json': { provider: 'cursor', auth_file: 'a.json', probed: false } } as never;
    const result = mergeProbeSummary(prior, 'a.json', { probed: true, checked_at: 'x', usable: 1, pruned: 2 });
    expect(result['a.json'].provider).toBe('cursor');
    expect(result['a.json'].pruned).toBe(2);
  });
});

describe('probe driver constants', () => {
  test('V1 drivers contained in the showcase set', () => {
    for (const provider of ['cursor', 'kiro', 'cline', 'claude', 'codex', 'xai', 'devin', 'meta']) {
      expect(PROBE_SUPPORTED_PROVIDERS.has(provider)).toBe(true);
    }
    expect(PROBE_SUPPORTED_PROVIDERS.has('gemini')).toBe(false);
    expect(PROBE_SUPPORTED_PROVIDERS.has('antigravity')).toBe(false);
  });
});

describe('formatCheckedAt', () => {
  test('returns empty for missing and truncates ISO to minutes', () => {
    expect(formatCheckedAt(undefined)).toBe('');
    expect(formatCheckedAt('')).toBe('');
    const sample = formatCheckedAt('2026-10-01T12:34:56Z');
    expect(sample).toMatch(/2026-10-01/);
    expect(sample).toMatch(/12:34|:34/);
  });
});

describe('modelProbeApi.run', () => {
  test('posts auth_index to /model-probe/run', async () => {
    const payload = { status: 'ok', summary: { probed: true, usable: 1, pruned: 0 }, models: [] };
    const post = spyOn(apiClient, 'post').mockResolvedValue(payload);
    try {
      const resp = await modelProbeApi.run('idx-1');
      expect(resp.summary?.probed).toBe(true);
      expect(post).toHaveBeenLastCalledWith('/model-probe/run', { auth_index: 'idx-1' }, undefined);
    } finally {
      post.mockRestore();
    }
  });

  test('status GET returns without params', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({ enabled: false, credentials: [] });
    try {
      const response = await modelProbeApi.status();
      expect(response.enabled).toBe(false);
      expect(get).toHaveBeenLastCalledWith('/model-probe/status', {});
    } finally {
      get.mockRestore();
    }
  });
});
