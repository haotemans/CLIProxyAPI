import { apiClient } from '@/services/api';
import type { UsageSummaryRow, UsageSeriesRow, UsageTotal } from './logic';

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
};
