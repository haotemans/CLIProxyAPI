import { apiClient } from '@/services/api';

export interface ModelProbeCredentialRow {
  auth_file?: string;
  auth_index?: string;
  provider?: string;
  label?: string;
  disabled?: boolean;
  driver?: boolean;
  probed?: boolean;
  checked_at?: string;
  usable?: number;
  pruned?: number;
  pruned_models?: string[];
}

export interface ModelProbeStatusResponse {
  enabled?: boolean;
  interval_seconds?: number;
  max_parallel?: number;
  supported_drivers?: string[];
  credentials?: ModelProbeCredentialRow[];
}

export interface ModelProbeRunModelRow {
  model?: string;
  status?: 'usable' | 'not_available' | 'limited' | 'auth_error' | 'unreachable';
  error?: string;
  checked_ms?: number;
}

export interface ModelProbeRunResponse {
  status?: string;
  summary?: { probed?: boolean; checked_at?: string; usable?: number; pruned?: number; pruned_models?: string[] };
  models?: ModelProbeRunModelRow[];
}

export const modelProbeApi = {
  status: (signal?: AbortSignal): Promise<ModelProbeStatusResponse> =>
    apiClient.get<ModelProbeStatusResponse>('/model-probe/status', {
      ...(signal ? { signal } : {}),
    }),

  run: (authIndex: string, signal?: AbortSignal): Promise<ModelProbeRunResponse> =>
    apiClient.post<ModelProbeRunResponse>(
      '/model-probe/run',
      { auth_index: authIndex },
      signal ? { signal } : undefined
    ),
};
