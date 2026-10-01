/**
 * Pool inspection (认证文件 巡检): types, API client and pure helpers for
 * grading credentials via GET /v0/management/pool-inspection. React-free
 * parts are consumed by tests/authFileInspection.test.ts.
 */
import { apiClient } from '@/services/api';

export type InspectionHealth = 'good' | 'warn' | 'bad' | string;

export interface InspectionSignals {
  disabled: boolean;
  status: string;
  status_message?: string;
  last_activity_ms?: number;
  quota_exceeded: boolean;
  quota_reason?: string;
  quota_recover_ms?: number;
  unauthorized: boolean;
  requests_24h: number;
  errors_24h: number;
  dominant_error?: string;
  probe_checked_at?: string;
  probe_auth_error: boolean;
  probe_skip_reason?: string;
}

export interface InspectionCredential {
  auth_file: string;
  provider: string;
  health: InspectionHealth;
  signals: InspectionSignals;
  suggestions: string[];
}

export interface InspectionResponse {
  credentials: InspectionCredential[];
  summary: { good: number; warn: number; bad: number } & Record<string, number>;
  window: string;
}

export const inspectionApi = {
  run: (provider?: string): Promise<InspectionResponse> =>
    apiClient.get<InspectionResponse>('/pool-inspection', {
      params: provider ? { provider } : {},
    }),
};

/** Index credentials by auth_file for O(1) card lookup. */
export const indexInspectionByFile = (
  credentials: InspectionCredential[] | undefined
): Record<string, InspectionCredential> => {
  const out: Record<string, InspectionCredential> = {};
  for (const cred of credentials ?? []) {
    if (cred?.auth_file) out[cred.auth_file] = cred;
  }
  return out;
};

/** Sort rank for chips: bad first, then warn, then good (stable tie on file). */
export const healthRank = (health: InspectionHealth): number =>
  health === 'bad' ? 0 : health === 'warn' ? 1 : 2;

/** The chip variant rendered for a health grade. */
export const healthChipVariant = (health: InspectionHealth): 'bad' | 'warn' | 'good' =>
  health === 'bad' ? 'bad' : health === 'warn' ? 'warn' : 'good';

/** Known suggestion codes (backend) — anything else renders verbatim. */
export const KNOWN_SUGGESTIONS = ['delete', 'relogin', 'rotate'] as const;
