import type { DistributionKeyRow, DistributionKeyWriteInput } from './api';

export type DistributionKeyStatus = 'active' | 'disabled' | 'expired';

/** Lifecycle: expired beats disabled (both show; expiry is terminal). */
export const distributionKeyStatus = (row: Pick<DistributionKeyRow, 'enabled' | 'expired'>): DistributionKeyStatus => {
  if (row.expired) return 'expired';
  if (!row.enabled) return 'disabled';
  return 'active';
};

/** Quota fill ratio 0..1 for the progress bar; null when unlimited. */
export const quotaUsageRatio = (quotaUsd: number | undefined, spentUsd: number): number | null => {
  if (quotaUsd === undefined || quotaUsd <= 0) return null;
  if (spentUsd < 0) return 0;
  const ratio = spentUsd / quotaUsd;
  return ratio > 1 ? 1 : ratio;
};

export interface DistributionFormValues {
  key: string;
  name: string;
  enabled: boolean;
  expiresAtLocal: string; // datetime-local input value, empty = never
  allowedModelsText: string; // one model id per line
  quotaUsdText: string; // empty = unlimited
}

export const emptyDistributionForm: DistributionFormValues = {
  key: '',
  name: '',
  enabled: true,
  expiresAtLocal: '',
  allowedModelsText: '',
  quotaUsdText: '',
};

export const distributionFormFromRow = (row: DistributionKeyRow): DistributionFormValues => ({
  key: '',
  name: row.name ?? '',
  enabled: row.enabled,
  expiresAtLocal: row.expires_at ? rfc3339ToLocalInput(row.expires_at) : '',
  allowedModelsText: (row.allowed_models ?? []).join('\n'),
  quotaUsdText: row.quota_usd && row.quota_usd > 0 ? String(row.quota_usd) : '',
});

/** datetime-local has no timezone: treat the value as local time, emit RFC3339. */
export const localInputToRfc3339 = (value: string): string => {
  const trimmed = value.trim();
  if (!trimmed) return '';
  const parsed = new Date(trimmed);
  if (Number.isNaN(parsed.getTime())) return '';
  return parsed.toISOString();
};

export const rfc3339ToLocalInput = (iso: string): string => {
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())}T${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`;
};

export const parseModelListText = (text: string): string[] =>
  [
    ...new Set(
      text
        .split(/[\n,]+/)
        .map((line) => line.trim())
        .filter(Boolean)
    ),
  ];

export interface DistributionFormError {
  field: 'expiresAt' | 'quota';
}

/** Validates edit/create form values; null means valid. */
export const validateDistributionForm = (
  values: DistributionFormValues,
  requireExpiryInFuture: boolean
): DistributionFormError | null => {
  const expiryIso = localInputToRfc3339(values.expiresAtLocal);
  if (values.expiresAtLocal.trim() !== '' && expiryIso === '') {
    return { field: 'expiresAt' };
  }
  if (requireExpiryInFuture && expiryIso !== '' && Date.parse(expiryIso) <= Date.now()) {
    return { field: 'expiresAt' };
  }
  const quotaText = values.quotaUsdText.trim();
  if (quotaText !== '') {
    const quota = Number(quotaText);
    if (!Number.isFinite(quota) || quota < 0) {
      return { field: 'quota' };
    }
  }
  return null;
};

/** Maps form values to the API write body (empty allowed-models = all models). */
export const distributionWritePayload = (values: DistributionFormValues, isCreate: boolean): DistributionKeyWriteInput => {
  const payload: DistributionKeyWriteInput = {
    name: values.name.trim(),
    enabled: values.enabled,
    expires_at: localInputToRfc3339(values.expiresAtLocal),
    allowed_models: parseModelListText(values.allowedModelsText),
    quota_usd: values.quotaUsdText.trim() === '' ? 0 : Number(values.quotaUsdText.trim()),
  };
  if (isCreate && values.key.trim() !== '') {
    payload.key = values.key.trim();
  }
  return payload;
};

/** Compact age/reset button label: last request is relative text-free ("never" handled by caller). */
export const hasQuota = (quotaUsd: number | undefined): boolean =>
  quotaUsd !== undefined && quotaUsd > 0;
