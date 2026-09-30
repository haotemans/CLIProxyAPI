import { describe, expect, test } from 'bun:test';
import {
  effectiveCostUSD,
  formatTokens,
  formatUSD,
  paletteForKey,
  presetRangeWindow,
  shapeSeriesByDay,
  sortSummaryDesc,
  type UsageSeriesRow,
  type UsageSummaryRow,
} from '@/features/usageStats/logic';

describe('presetRangeWindow', () => {
  test('1d / 7d / 30d offsets', () => {
    const now = 1_800_000_000_000;
    expect(presetRangeWindow('24h', now)).toEqual({ from: now - 86400000, to: now });
    expect(presetRangeWindow('7d', now)).toEqual({ from: now - 7 * 86400000, to: now });
    expect(presetRangeWindow('30d', now)).toEqual({ from: now - 30 * 86400000, to: now });
  });
});

describe('effectiveCostUSD', () => {
  test('upstream wins when > 0', () => {
    expect(effectiveCostUSD(0.5, 9)).toBe(0.5);
  });
  test('falls back to computed when upstream is zero', () => {
    expect(effectiveCostUSD(0, 2)).toBe(2);
  });
  test('zero when nothing known', () => {
    expect(effectiveCostUSD(0, 0)).toBe(0);
  });
});

describe('formatUSD / formatTokens', () => {
  test('money rounding', () => {
    expect(formatUSD(0.0123)).toBe('$0.0123');
    expect(formatUSD(1.234)).toBe('$1.23');
    expect(formatUSD(123.456)).toBe('$123');
    expect(formatUSD(0)).toBe('$0.00');
  });
  test('compact tokens', () => {
    expect(formatTokens(999)).toBe('999');
    expect(formatTokens(1500)).toBe('1.5K');
    expect(formatTokens(2_300_000)).toBe('2.3M');
  });
});

describe('sortSummaryDesc', () => {
  test('cost desc then requests desc', () => {
    const rows: UsageSummaryRow[] = [
      { key: 'a', requests: 10, input_tokens: 1, output_tokens: 1, cached_tokens: 0, total_tokens: 2, upstream_cost_usd: 0, computed_cost_usd: 0.01 },
      { key: 'b', requests: 5, input_tokens: 1, output_tokens: 1, cached_tokens: 0, total_tokens: 2, upstream_cost_usd: 0.04, computed_cost_usd: 0 },
      { key: 'c', requests: 99, input_tokens: 1, output_tokens: 1, cached_tokens: 0, total_tokens: 2, upstream_cost_usd: 0, computed_cost_usd: 0.001 },
    ];
    const sorted = sortSummaryDesc(rows);
    expect(sorted.map((r) => r.key)).toEqual(['b', 'a', 'c']);
  });
});

describe('shapeSeriesByDay', () => {
  test('fills the window, stacks per key, skips missing keys per day', () => {
    const from = Date.parse('2026-09-28T00:00:00Z');
    const to = Date.parse('2026-09-30T23:59:59Z');
    const rows: UsageSeriesRow[] = [
      { bucket: '2026-09-28', key: 'claude', requests: 1, input_tokens: 1, output_tokens: 1, total_tokens: 2, upstream_cost_usd: 0.02, computed_cost_usd: 0 },
      { bucket: '2026-09-28', key: 'kiro', requests: 1, input_tokens: 1, output_tokens: 1, total_tokens: 2, upstream_cost_usd: 0, computed_cost_usd: 0.01 },
      { bucket: '2026-09-29', key: 'claude', requests: 2, input_tokens: 1, output_tokens: 1, total_tokens: 2, upstream_cost_usd: 0.03, computed_cost_usd: 0 },
    ];
    const model = shapeSeriesByDay(rows, from, to);
    expect(model.keys).toEqual(['claude', 'kiro']);
    expect(model.days.length).toBeGreaterThanOrEqual(3);
    const d28 = model.days.find((d) => d.bucket === '2026-09-28');
    expect(d28?.total).toBeCloseTo(0.03);
    expect(d28?.segments.map((s) => s.key)).toEqual(['claude', 'kiro']);
    const d29 = model.days.find((d) => d.bucket === '2026-09-29');
    expect(d29?.segments).toHaveLength(1);
    expect(model.maxTotal).toBeCloseTo(0.03);
  });

  test('returns zero-valued days when untouched', () => {
    const from = Date.parse('2026-09-28T00:00:00Z');
    const to = Date.parse('2026-09-29T23:59:59Z');
    const model = shapeSeriesByDay([], from, to);
    expect(model.maxTotal).toBe(0);
    expect(model.days.length).toBeGreaterThan(0);
  });

  test('custom valueOf can key charts on tokens instead of cost', () => {
    const from = Date.parse('2026-09-28T00:00:00Z');
    const to = Date.parse('2026-09-28T23:59:59Z');
    const rows: UsageSeriesRow[] = [
      { bucket: '2026-09-28', key: 'claude', requests: 1, input_tokens: 5, output_tokens: 7, total_tokens: 12, upstream_cost_usd: 0, computed_cost_usd: 0 },
    ];
    const model = shapeSeriesByDay(rows, from, to, (row) => row.total_tokens);
    expect(model.maxTotal).toBe(12);
  });
});

describe('paletteForKey', () => {
  test('stable per key', () => {
    expect(paletteForKey('claude', 0)).toBe(paletteForKey('claude', 0));
    expect(paletteForKey('claude', 0)).toMatch(/^#/);
  });
});
