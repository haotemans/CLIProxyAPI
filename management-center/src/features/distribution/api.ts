import { apiClient } from '@/services/api';

export interface DistributionKeyRow {
  key_hash: string;
  key_masked: string;
  name?: string;
  enabled: boolean;
  expires_at?: string;
  expired?: boolean;
  allowed_models?: string[];
  quota_usd?: number;
  spent_usd: number;
  requests: number;
  last_request_at?: string;
}

export interface DistributionKeyCreateResponse {
  'distribution-key': DistributionKeyRow;
  key: string;
  note?: string;
}

export interface DistributionKeyWriteInput {
  key?: string;
  name?: string;
  enabled?: boolean;
  expires_at?: string;
  allowed_models?: string[];
  quota_usd?: number;
}

export interface DistributionUsageRow {
  key_hash: string;
  key_masked: string;
  name?: string;
  quota_usd?: number;
  spent_usd: number;
  requests: number;
  last_request_at?: string;
  window?: {
    key: string;
    requests: number;
    input_tokens: number;
    output_tokens: number;
    upstream_cost_usd: number;
    computed_cost_usd: number;
  };
}

export const distributionKeysApi = {
  list: (signal?: AbortSignal): Promise<{ 'distribution-keys': DistributionKeyRow[] }> =>
    apiClient.get<{ 'distribution-keys': DistributionKeyRow[] }>('/distribution-keys', {
      ...(signal ? { signal } : {}),
    }),

  create: (input: DistributionKeyWriteInput): Promise<DistributionKeyCreateResponse> =>
    apiClient.post<DistributionKeyCreateResponse>('/distribution-keys', input),

  update: (keyHash: string, input: DistributionKeyWriteInput): Promise<{ 'distribution-key': DistributionKeyRow }> =>
    apiClient.put<{ 'distribution-key': DistributionKeyRow }>(
      `/distribution-keys?key-hash=${encodeURIComponent(keyHash)}`,
      input
    ),

  remove: (keyHash: string): Promise<{ status: string }> =>
    apiClient.delete<{ status: string }>(
      `/distribution-keys?key-hash=${encodeURIComponent(keyHash)}`
    ),

  resetUsage: (keyHash: string): Promise<{ status: string }> =>
    apiClient.post<{ status: string }>('/distribution-keys/reset-usage', { key_hash: keyHash }),

  usage: (signal?: AbortSignal): Promise<{ rows: DistributionUsageRow[]; from: number; to: number }> =>
    apiClient.get<{ rows: DistributionUsageRow[]; from: number; to: number }>(
      '/distribution-keys/usage',
      { ...(signal ? { signal } : {}) }
    ),
};
