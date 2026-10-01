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
  /** text = static translated fragment; usable/pruned/removed/date = rendered values. */
  kind: 'text' | 'usable' | 'pruned' | 'removed' | 'date';
  text: string;
}

/**
 * Splits a localized probe line into parts while keeping every localized word
 * intact. The locale template is resolved through the normal i18n call with
 * sentinel placeholders so the user's locale file never changes; counts and
 * the date become separately styleable spans (status colors apply only to
 * non-zero counts).
 */
const splitSentinelTemplate = <K extends 'usable' | 'pruned' | 'removed' | 'date'>(
  template: string,
  tokens: readonly { kind: K; token: string }[],
  valueByKind: Record<K, string>
): ProbeSummaryPart[] => {
  const tokenToKind = new Map<string, K>(tokens.map((item) => [item.token, item.kind]));
  const pattern = new RegExp(tokens.map((item) => escapeProbeSentinel(item.token)).join('|'), 'g');
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

export const splitProbeSummary = (
  render: (values: { usable: string; pruned: string; date: string }) => string,
  usable: number,
  pruned: number,
  date: string
): ProbeSummaryPart[] =>
  splitSentinelTemplate(
    render({ usable: '@@U@@', pruned: '@@P@@', date: '@@D@@' }),
    [
      { kind: 'usable', token: '@@U@@' },
      { kind: 'pruned', token: '@@P@@' },
      { kind: 'date', token: '@@D@@' },
    ],
    { usable: String(usable), pruned: String(pruned), date }
  );

/** Splits the aggressive prune result line ("usable n · removed m"). */
export const splitPruneResult = (
  render: (values: { usable: string; removed: string }) => string,
  usable: number,
  removed: number
): ProbeSummaryPart[] =>
  splitSentinelTemplate(
    render({ usable: '@@U@@', removed: '@@R@@' }),
    [
      { kind: 'usable', token: '@@U@@' },
      { kind: 'removed', token: '@@R@@' },
    ],
    { usable: String(usable), removed: String(removed) }
  );

const escapeProbeSentinel = (value: string): string =>
  value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** Whether the provider has a backend driver for the per-model test action. */
export const canTestModelProvider = (provider: string | undefined | null): boolean =>
  PROBE_SUPPORTED_PROVIDERS.has((provider ?? '').trim().toLowerCase());

export interface ModelTestOutcome {
  state: 'running' | 'done';
  ok?: boolean;
  status?: string;
  error?: string;
  reply_text?: string;
  latency_ms?: number;
}

export interface ModelTestResultView {
  tone: 'running' | 'ok' | 'error';
  text: string;
}

/** Inline result line for one model row: muted running label, green reply +
 * latency on success, red status/error otherwise. Null when never run. */
export const modelTestResultView = (
  outcome: ModelTestOutcome | undefined,
  runningLabel: string
): ModelTestResultView | null => {
  if (!outcome) return null;
  if (outcome.state === 'running') return { tone: 'running', text: runningLabel };
  if (outcome.ok) {
    const reply = (outcome.reply_text ?? '').trim() || '—';
    const latency =
      typeof outcome.latency_ms === 'number' && Number.isFinite(outcome.latency_ms)
        ? ` · ${Math.round(outcome.latency_ms)} ms`
        : '';
    return { tone: 'ok', text: reply + latency };
  }
  const prefix = (outcome.status ?? '').trim() ? `${(outcome.status ?? '').trim()}: ` : '';
  return { tone: 'error', text: prefix + (outcome.error ?? '').trim() };
};
