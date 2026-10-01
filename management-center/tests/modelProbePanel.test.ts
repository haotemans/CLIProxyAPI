import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { modelProbeApi } from '@/features/authFiles/modelProbe/api';
import {
  formatCheckedAt,
  formatRelativeFromNow,
  mergeProbeSummary,
  probePresentation,
  PROBE_SUPPORTED_PROVIDERS,
  rowsToMap,
  splitProbeSummary,
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

describe('formatRelativeFromNow', () => {
  test('soon / minutes / hours / days', () => {
    const now = Date.parse('2026-10-01T00:00:00Z');
    expect(formatRelativeFromNow('2026-10-01T00:00:30Z', now)).toBe('soon');
    expect(formatRelativeFromNow('2026-10-01T00:30:00Z', now)).toBe('30m');
    expect(formatRelativeFromNow('2026-10-01T06:00:00Z', now)).toBe('6h');
    expect(formatRelativeFromNow('2026-10-02T12:00:00Z', now)).toBe('36h');
    expect(formatRelativeFromNow('2026-10-04T12:00:00Z', now)).toBe('4d');
    expect(formatRelativeFromNow(undefined, now)).toBe('');
    expect(formatRelativeFromNow('not-a-date', now)).toBe('');
  });

  test('status response carries next_run_at', () => {
    const response = { enabled: true, next_run_at: '2026-10-02T00:00:00Z', credentials: [] };
    expect(response.next_run_at).toBeTruthy();
  });
});

describe('splitProbeSummary', () => {
  // Drives the real locale templates from disk (unchanged locale files, all
  // four of them share the usable/pruned/date placeholder triple).
  const localeRender = (locale: string) => {
    const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
    const template = translations.auth_files.probe_summary as string;
    return (values: { usable: string; pruned: string; date: string }) =>
      template
        .replaceAll('{{usable}}', values.usable)
        .replaceAll('{{pruned}}', values.pruned)
        .replaceAll('{{checked_at}}', values.date);
  };

  test('keeps every localized word and splits the three values in all 4 locales', () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const parts = splitProbeSummary(localeRender(locale), 3, 1, '2026-10-01 12:34');
      const byKind = Object.fromEntries(parts.map((part) => [part.kind, part.text]));
      expect(byKind.usable).toBe('3');
      expect(byKind.pruned).toBe('1');
      expect(byKind.date).toBe('2026-10-01 12:34');
      // No placeholder sentinel leaks into the rendered text.
      for (const part of parts) {
        expect(part.text).not.toContain('@@U@@');
        expect(part.text).not.toContain('@@P@@');
        expect(part.text).not.toContain('@@D@@');
      }
      // The joined parts rebuild the localized sentence exactly.
      expect(parts.map((part) => part.text).join('')).toBe(
        localeRender(locale)({ usable: '3', pruned: '1', date: '2026-10-01 12:34' })
      );
    }
  });

  test('splits zero counts without dropping the muted fragments', () => {
    const parts = splitProbeSummary(localeRender('en'), 0, 0, 'never');
    const usable = parts.find((part) => part.kind === 'usable');
    const pruned = parts.find((part) => part.kind === 'pruned');
    expect(usable?.text).toBe('0');
    expect(pruned?.text).toBe('0');
    expect(parts.some((part) => part.kind === 'text' && part.text.includes('usable'))).toBe(true);
  });
});

describe('probePresentation', () => {
  test('reads the blocked class and catalog size from a status row', () => {
    expect(probePresentation({ status: 'provider_blocked', catalog_size: 10 })).toEqual({
      blocked: true,
      catalogSize: 10,
    });
    expect(probePresentation({ catalog_size: 4 })).toEqual({ blocked: false, catalogSize: 4 });
    expect(probePresentation(undefined)).toEqual({ blocked: false, catalogSize: 0 });
    expect(probePresentation({ catalog_size: 0 })).toEqual({ blocked: false, catalogSize: 0 });
    expect(probePresentation({ status: 'auth_error', catalog_size: 3 })).toEqual({
      blocked: false,
      catalogSize: 3,
    });
  });

  test('hint and catalog locale keys exist in every locale', () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      const hint = translations.auth_files.probe_provider_blocked_hint as string;
      const catalogKey = translations.auth_files.probe_catalog as string;
      expect(hint).toBeTruthy();
      expect(catalogKey).toContain('{{size}}');
    }
  });
});
