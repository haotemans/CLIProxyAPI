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
