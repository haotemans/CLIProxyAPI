import type { AuthFileItem, NativeQuotaState } from '@/types';
import { apiClient } from '@/services/api';
import { normalizeAuthIndex } from '@/utils/authIndex';

interface QuotaBucketPayload {
  window?: string;
  remainingFraction?: number;
  remaining_fraction?: number;
  resetTime?: string;
  reset_time?: string;
  description?: string;
}

interface QuotaGroupPayload {
  displayName?: string;
  display_name?: string;
  buckets?: QuotaBucketPayload[];
}

interface QuotaSubscriptionPayload {
  plan?: string;
  tierName?: string;
  tier_name?: string;
}

export interface QuotaFetchPayload {
  subscription?: QuotaSubscriptionPayload;
  groups?: QuotaGroupPayload[];
}

export interface NativeQuotaData {
  plan?: string;
  rows: NativeQuotaState['rows'];
}

const pick = <T>(...values: T[]): T | undefined => {
  for (const value of values) if (value !== undefined) return value;
  return undefined;
};

const parseResetMs = (value?: string): number | null => {
  if (!value || !value.trim()) return null;
  const ms = Date.parse(value);
  return Number.isFinite(ms) ? ms : null;
};

/** Map the normalized quota payload onto generic meter rows for native lanes. */
export function readNativeQuotaData(payload: QuotaFetchPayload): NativeQuotaData {
  const plan =
    payload.subscription?.plan?.trim() ||
    payload.subscription?.tierName?.trim() ||
    payload.subscription?.tier_name?.trim() ||
    '';
  const rows: NativeQuotaState['rows'] = [];
  const groups = Array.isArray(payload.groups) ? payload.groups : [];
  groups.forEach((group, groupIndex) => {
    const buckets = Array.isArray(group.buckets) ? group.buckets : [];
    buckets.forEach((bucket, bucketIndex) => {
      const fraction = pick(bucket.remainingFraction, bucket.remaining_fraction);
      if (typeof fraction !== 'number' || !Number.isFinite(fraction)) return;
      const clamped = Math.min(1, Math.max(0, fraction));
      const limit = 100;
      const used = (1 - clamped) * 100;
      const displayName = pick(group.displayName, group.display_name)?.trim() || '';
      const description = bucket.description?.trim();
      const label = description ? (displayName ? `${displayName} · ${description}` : description) : displayName;
      rows.push({
        id: `${groupIndex}-${bucketIndex}:${displayName || bucket.window || 'quota'}`,
        label: label || bucket.window || `${displayName || 'quota'} ${bucketIndex + 1}`,
        used,
        limit,
        resetAtMs: parseResetMs(pick(bucket.resetTime, bucket.reset_time)),
      });
    });
  });
  return { plan: plan || undefined, rows };
}

export class NativeQuotaError extends Error {
  constructor(
    public readonly code: 'missing_auth_index' | 'request_failed',
    public readonly status?: number
  ) {
    super(code);
    this.name = 'NativeQuotaError';
  }
}

/** Auth index is required so the backend resolves the credential itself. */
export async function fetchNativeQuota(file: AuthFileItem): Promise<NativeQuotaData> {
  const authIndex = normalizeAuthIndex(file.authIndex ?? file.auth_index);
  if (!authIndex) throw new NativeQuotaError('missing_auth_index');
  let payload: QuotaFetchPayload;
  try {
    payload = await apiClient.post<QuotaFetchPayload>('/quota/fetch', { auth_index: authIndex });
  } catch (error: unknown) {
    const status =
      error !== null && typeof error === 'object' && typeof (error as { status?: unknown }).status === 'number'
        ? (error as { status: number }).status
        : undefined;
    throw new NativeQuotaError('request_failed', status);
  }
  return readNativeQuotaData(payload);
}
