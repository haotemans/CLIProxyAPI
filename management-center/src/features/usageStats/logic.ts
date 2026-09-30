/**
 * Usage-stats page pure logic: window computation, series bucketing,
 * chart stack shaping, money cost selection and table sorting.
 * React-free — directly consumed by tests/usageStatsLogic.test.ts.
 */

export type UsageRangePreset = '24h' | '7d' | '30d' | 'custom';

export interface UsageWindow {
  from: number;
  to: number;
}

export const presetRangeWindow = (preset: UsageRangePreset, now: number): UsageWindow => {
  const days = preset === '7d' ? 7 : preset === '30d' ? 30 : 1;
  return { from: now - days * 24 * 3600 * 1000, to: now };
};

export interface UsageTotal {
  requests: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  total_tokens: number;
  upstream_cost_usd: number;
  computed_cost_usd: number;
}

export interface UsageSummaryRow extends UsageTotal {
  key: string;
}

export interface UsageSeriesRow {
  bucket: string;
  key: string;
  requests: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  upstream_cost_usd: number;
  computed_cost_usd: number;
}

/** Money: prefer upstream-reported spend when > 0, else the computed price. */
export const effectiveCostUSD = (upstream: number, computed: number): number =>
  upstream > 0 ? upstream : computed > 0 ? computed : 0;

/** Display money: full precision small values, cents beyond the dollar mark. */
export const formatUSD = (value: number): string => {
  if (!Number.isFinite(value)) return '-';
  if (value === 0) return '$0.00';
  if (value < 0) return `-$${formatUSDNumber(-value)}`;
  return `$${formatUSDNumber(value)}`;
};

const formatUSDNumber = (value: number): string => {
  if (value < 1) return value.toFixed(4).replace(/0+$/, '').replace(/\.$/, '');
  if (value < 100) return value.toFixed(2).replace(/0+$/, '').replace(/\.$/, '');
  return value.toFixed(0);
};

/** Compact token counts for cards/table (1.2K, 3.4M). */
export const formatTokens = (value: number): string => {
  if (!Number.isFinite(value) || value <= 0) return '0';
  if (value >= 1_000_000) return `${formatSI(value / 1_000_000)}M`;
  if (value >= 1_000) return `${formatSI(value / 1_000)}K`;
  return String(Math.trunc(value));
};

const formatSI = (value: number): string =>
  value >= 100 ? value.toFixed(0) : value.toFixed(1).replace(/\.0$/, '');

export const sortSummaryDesc = (rows: UsageSummaryRow[]): UsageSummaryRow[] =>
  [...rows].sort((a, b) => {
    const costDiff = effectiveCostUSD(b.upstream_cost_usd, b.computed_cost_usd) - effectiveCostUSD(a.upstream_cost_usd, a.computed_cost_usd);
    if (costDiff !== 0) return costDiff;
    return b.requests - a.requests;
  });

export interface ChartDayPoint {
  bucket: string;
  total: number;
  segments: { key: string; value: number }[];
}

export interface ChartModel {
  days: ChartDayPoint[];
  keys: string[];
  maxTotal: number;
}

const DAY_MS = 24 * 3600 * 1000;

/**
 * Shape the daily series into per-day stacked buckets. Days with zero
 * activity are still emitted so the chart reads as a continuous range.
 */
export const shapeSeriesByDay = (
  rows: UsageSeriesRow[],
  from: number,
  to: number,
  valueOf: (row: UsageSeriesRow) => number = (row) => effectiveCostUSD(row.upstream_cost_usd, row.computed_cost_usd)
): ChartModel => {
  const perDay = new Map<string, Map<string, number>>();
  const keys: string[] = [];
  const keyIndex = new Map<string, number>();
  for (const row of rows) {
    if (!row || !row.bucket || !row.key) continue;
    if (keyIndex.get(row.key) === undefined) {
      keyIndex.set(row.key, keys.length);
      keys.push(row.key);
    }
    const day = row.bucket.slice(0, 10);
    let dayMap = perDay.get(day);
    if (!dayMap) {
      dayMap = new Map();
      perDay.set(day, dayMap);
    }
    dayMap.set(row.key, (dayMap.get(row.key) ?? 0) + valueOf(row));
  }
  const days: ChartDayPoint[] = [];
  let maxTotal = 0;
  const dayCount = Math.max(1, Math.round((to - from) / DAY_MS));
  for (let i = 0; i <= dayCount && i < 62; i++) {
    const day = formatBucketDate(from + i * DAY_MS);
    const segments: { key: string; value: number }[] = [];
    let total = 0;
    const dayMap = perDay.get(day);
    for (const key of keys) {
      const value = dayMap?.get(key) ?? 0;
      if (value > 0) segments.push({ key, value });
      total += value;
    }
    if (segments.length > 0 || dayMap) {
      days.push({ bucket: day, total, segments });
      if (total > maxTotal) maxTotal = total;
    } else {
      days.push({ bucket: day, total: 0, segments: [] });
    }
  }
  // Drop trailing zero-total days that exceed the covered range tightly: keep the
  // full [from..to] window, ending exactly on today even when untouched.
  const lastCovered = formatBucketDate(to);
  while (days.length > 0 && days[days.length - 1].bucket > lastCovered) {
    days.pop();
  }
  return { days, keys, maxTotal };
};

const formatBucketDate = (ms: number): string => {
  const d = new Date(ms);
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return `${y}-${m}-${day}`;
};

/** Distinct stable-ish color class per key, cycled through a small palette. */
export const CHART_PALETTE = [
  '#5b8dd9',
  '#d97aa8',
  '#59b08a',
  '#d99a5b',
  '#8f7ad9',
  '#c96ed9',
  '#d9c564',
  '#64b8d9',
  '#d96f6f',
  '#4fd9b8',
  '#93d0ff',
  '#bd8df0',
] as const;

export const paletteForKey = (key: string, index: number): string =>
  CHART_PALETTE[Math.abs(hashKey(key) + index) % CHART_PALETTE.length];

const hashKey = (key: string): number => {
  let hash = 0;
  for (let i = 0; i < key.length; i++) {
    hash = (hash * 31 + key.charCodeAt(i)) & 0x7fffffff;
  }
  return hash;
};
