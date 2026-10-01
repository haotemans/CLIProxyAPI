import type { ModelProbeCredentialRow } from './api';

/** Providers with V1 probe drivers in the backend (mirrors internal/modelprobe). */
export const PROBE_SUPPORTED_PROVIDERS = new Set([
  'cursor',
  'kiro',
  'cline',
  'claude',
  'codex',
  'xai',
  'devin',
  'meta',
  'mirasim',
  'commandcode',
  'opencode-go',
]);

export type ProbeRowByFile = Record<string, ModelProbeCredentialRow>;

/** Shape the status response into an auth_file-keyed lookup. */
export function rowsToMap(rows: ModelProbeCredentialRow[]): ProbeRowByFile {
  const map: ProbeRowByFile = {};
  for (const row of rows) {
    const name = (row.auth_file ?? '').trim();
    if (!name) continue;
    map[name] = row;
  }
  return map;
}

export interface ProbeSummaryShape {
  probed: boolean;
  checked_at: string;
  usable: number;
  pruned: number;
  pruned_models?: string[];
}

/** Merge the run response summary into an existing map row (or create one). */
export function mergeProbeSummary(
  existing: ProbeRowByFile,
  file: string,
  summary: ProbeSummaryShape
): ProbeRowByFile {
  const previous = existing[file];
  return {
    ...existing,
    [file]: {
      ...previous,
      auth_file: file,
      probed: summary.probed,
      checked_at: summary.checked_at,
      usable: summary.usable,
      pruned: summary.pruned,
      pruned_models: summary.pruned_models,
    },
  };
}

/** Reset time label: ISO date-time trunc to minutes, empty when missing. */
export const formatCheckedAt = (iso: string | undefined): string => {
  if (!iso) return '';
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return iso;
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

/** status class reported by the backend when the provider closes third-party access. */
export const PROVIDER_BLOCKED_STATUS = 'provider_blocked';

export interface ProbePresentation {
  /** True when every recorded outcome is the provider-block phase (wait, no relogin). */
  blocked: boolean;
  /** Curated catalog size from the last probe cycle (0 when never recorded). */
  catalogSize: number;
}

/** Reads the bucket-aware, block-aware presentation contract from a status row. */
export const probePresentation = (row: { status?: string; catalog_size?: number } | undefined): ProbePresentation => ({
  blocked: (row?.status ?? '').trim() === PROVIDER_BLOCKED_STATUS,
  catalogSize:
    typeof row?.catalog_size === 'number' && Number.isFinite(row.catalog_size) && row.catalog_size > 0
      ? Math.trunc(row.catalog_size)
      : 0,
});

/**
 * Relative "in ~X" label for the next scheduled probe (jitter-aware):
 * "~2h", "~9h", "~2d", or "soon" within the next minute.
 */
export const formatRelativeFromNow = (iso: string | undefined, now: number): string => {
  if (!iso) return '';
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return '';
  const diffMs = ms - now;
  if (diffMs < 60_000) return 'soon';
  const hours = diffMs / 3_600_000;
  if (hours < 1) return `${Math.max(1, Math.round(diffMs / 60_000))}m`;
  if (hours < 48) return `${Math.round(hours)}h`;
  return `${Math.round(hours / 24)}d`;
};

/** One piece of the localized probe summary line. */
export interface ProbeSummaryPart {
  /** text = static translated fragment; usable/pruned/date = rendered values. */
  kind: 'text' | 'usable' | 'pruned' | 'date';
  text: string;
}

const PROBE_SENTINEL_TOKENS = [
  { kind: 'usable' as const, token: '@@U@@' },
  { kind: 'pruned' as const, token: '@@P@@' },
  { kind: 'date' as const, token: '@@D@@' },
] as const;

/**
 * Splits the probe_summary translation into parts while keeping every
 * localized word intact. The locale template is resolved through the normal
 * i18n call with sentinel placeholders so the user's locale file never
 * changes; counts and the date become separately styleable spans (status
 * colors apply only to non-zero counts).
 */
export const splitProbeSummary = (
  render: (values: { usable: string; pruned: string; date: string }) => string,
  usable: number,
  pruned: number,
  date: string
): ProbeSummaryPart[] => {
  const template = render({
    usable: PROBE_SENTINEL_TOKENS[0].token,
    pruned: PROBE_SENTINEL_TOKENS[1].token,
    date: PROBE_SENTINEL_TOKENS[2].token,
  });
  const valueByKind: Record<'usable' | 'pruned' | 'date', string> = {
    usable: String(usable),
    pruned: String(pruned),
    date,
  };
  const tokenToKind = new Map<string, 'usable' | 'pruned' | 'date'>(
    PROBE_SENTINEL_TOKENS.map((item) => [item.token, item.kind])
  );
  const pattern = new RegExp(
    PROBE_SENTINEL_TOKENS.map((item) => escapeProbeSentinel(item.token)).join('|'),
    'g'
  );
  const parts: ProbeSummaryPart[] = [];
  let last = 0;
  for (const match of template.matchAll(pattern)) {
    const index = match.index ?? 0;
    if (index > last) parts.push({ kind: 'text', text: template.slice(last, index) });
    const token = match[0];
    const kind = tokenToKind.get(token);
    if (kind) parts.push({ kind, text: valueByKind[kind] });
    last = index + token.length;
  }
  if (last < template.length) parts.push({ kind: 'text', text: template.slice(last) });
  return parts.filter((part) => part.kind !== 'text' || part.text !== '');
};

const escapeProbeSentinel = (value: string): string =>
  value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
