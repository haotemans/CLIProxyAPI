/**
 * Request-events + pricing tab pure logic. React-free — directly consumed by
 * tests (tests/usageStatsEvents.test.ts, tests/usageStatsPricing.test.ts).
 */

export interface UsageEventRow {
  id: number;
  ts: number;
  provider: string;
  auth_file: string;
  api_key: string;
  endpoint: string;
  model: string;
  upstream_model: string;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  total_tokens: number;
  upstream_cost_usd: number;
  computed_cost_usd: number;
  status: string;
  latency_ms: number;
  error: string;
}

export interface UsageEventFilter {
  from: number;
  to: number;
  provider: string;
  model: string;
  authFile: string;
  status: '' | 'ok' | 'error';
  page: number;
  pageSize: number;
}

export const DEFAULT_EVENTS_PAGE_SIZE = 50;

export const defaultEventFilter = (from: number, to: number): UsageEventFilter => ({
  from,
  to,
  provider: '',
  model: '',
  authFile: '',
  status: '',
  page: 1,
  pageSize: DEFAULT_EVENTS_PAGE_SIZE,
});

/** Total pages for a filtered listing (at least 1 so the pager never shows 0). */
export const totalEventPages = (total: number, pageSize: number): number => {
  if (pageSize <= 0) return 1;
  return Math.max(1, Math.ceil(Math.max(0, total) / pageSize));
};

/** Clamp a requested page into [1, pages]. */
export const clampEventPage = (page: number, pages: number): number =>
  Math.min(Math.max(1, Math.trunc(page)), Math.max(1, pages));

/** Timestamp cell for the table (local, second precision). */
export const formatEventTs = (tsMs: number): string => {
  if (!Number.isFinite(tsMs) || tsMs <= 0) return '-';
  const d = new Date(tsMs);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
};

/** Compact latency (123ms / 1.2s). */
export const formatLatency = (ms: number): string => {
  if (!Number.isFinite(ms) || ms < 0) return '-';
  if (ms < 1000) return `${Math.trunc(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
};

/* ------------------------------ pricing tab ------------------------------ */

export interface PricingViewEntry {
  model: string;
  input: number;
  output: number;
  source: string;
}

/**
 * Case-insensitive substring search over the merged pricing view. Matching
 * keeps the input order (backend returns model-sorted rows).
 */
export const filterPricingEntries = (entries: PricingViewEntry[], search: string): PricingViewEntry[] => {
  const needle = search.trim().toLowerCase();
  if (!needle) return entries;
  return entries.filter((entry) => entry.model.toLowerCase().includes(needle));
};

/**
 * Validation for the inline price edit: both rates must be finite numbers
 * >= 0 (0 means "free", which is allowed intentionally).
 */
export const isValidPriceInput = (input: string, output: string): boolean =>
  [input, output].every((raw) => {
    const value = Number(raw.trim());
    return raw.trim() !== '' && Number.isFinite(value) && value >= 0;
  });

/** Price cell formatting: up to 4 significant decimals, trailing zeros cut. */
export const formatPrice = (value: number): string => {
  if (!Number.isFinite(value)) return '-';
  if (value === 0) return '$0';
  if (value < 0.01) return `$${value.toPrecision(2)}`;
  return `$${String(Math.round(value * 10000) / 10000)}`;
};
