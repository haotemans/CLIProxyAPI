import { apiClient } from '@/services/api';
import type { UsageSummaryRow, UsageSeriesRow, UsageTotal } from './logic';
import type { UsageEventFilter, UsageEventRow } from './eventsLogic';

export type UsageGroupBy = 'provider' | 'model' | 'auth_file' | 'api_key' | 'day';

export interface UsageMetersResponse<R> {
  enabled: boolean;
  rows: R[];
  interval?: string;
  group_by?: string;
  from?: number;
  to?: number;
  dropped?: number;
}

export interface UsageSummaryResponse extends UsageMetersResponse<UsageSummaryRow> {
  totals: UsageTotal;
}

export interface UsageEventsResponse {
  enabled: boolean;
  rows: UsageEventRow[];
  total: number;
  page: number;
  page_size: number;
}

export interface PricingEntry {
  model: string;
  input: number;
  output: number;
  /** default | user | litellm */
  source: 'default' | 'user' | 'litellm' | string;
}

export interface PricingResponse {
  entries: PricingEntry[];
}

export interface PricingSyncResult {
  synced: number;
  added: number;
  updated: number;
  skipped_user: number;
  skipped_invalid: number;
  catalog_size: number;
}

export const usageStatsApi = {
  summary: (params: {
    from: number;
    to: number;
    group_by: UsageGroupBy;
    signal?: AbortSignal;
  }): Promise<UsageSummaryResponse> =>
    apiClient.get<UsageSummaryResponse>('/usage-meters/summary', {
      params: { from: params.from, to: params.to, group_by: params.group_by },
      ...(params.signal ? { signal: params.signal } : {}),
    }),

  series: (params: {
    from: number;
    to: number;
    interval: 'hour' | 'day';
    group_by: 'provider' | 'model';
    signal?: AbortSignal;
  }): Promise<UsageMetersResponse<UsageSeriesRow>> =>
    apiClient.get<UsageMetersResponse<UsageSeriesRow>>('/usage-meters/series', {
      params: {
        from: params.from,
        to: params.to,
        interval: params.interval,
        group_by: params.group_by,
      },
      ...(params.signal ? { signal: params.signal } : {}),
    }),

  events: (filter: UsageEventFilter): Promise<UsageEventsResponse> =>
    apiClient.get<UsageEventsResponse>('/usage-meters/events', {
      params: buildEventsParams(filter),
    }),

  pricing: (): Promise<PricingResponse> => apiClient.get<PricingResponse>('/usage-meters/pricing'),

  putPrice: (model: string, input: number, output: number): Promise<unknown> =>
    apiClient.put(`/usage-meters/pricing/_`, { input, output }, { params: { model } }),

  deletePrice: (model: string): Promise<unknown> =>
    apiClient.delete(`/usage-meters/pricing/_`, { params: { model } }),

  syncLitellm: (): Promise<PricingSyncResult> =>
    apiClient.post<PricingSyncResult>('/usage-meters/pricing/sync-litellm', {}),
};

/** Serializes the events filter; empty fields are dropped. */
export const buildEventsParams = (filter: UsageEventFilter): Record<string, string | number> => {
  const params: Record<string, string | number> = {
    page: filter.page,
    page_size: filter.pageSize,
  };
  if (filter.from) params.from = filter.from;
  if (filter.to) params.to = filter.to;
  if (filter.provider.trim()) params.provider = filter.provider.trim();
  if (filter.model.trim()) params.model = filter.model.trim();
  if (filter.authFile.trim()) params.auth_file = filter.authFile.trim();
  if (filter.status) params.status = filter.status;
  return params;
};
