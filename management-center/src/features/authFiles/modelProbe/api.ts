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
  /** Curated catalog size the last cycle probed (bucket-aware counter). */
  catalog_size?: number;
  /** Optional credential-level mark: 'provider_blocked' when the provider
   * closes third-party access for the account tier (e.g. Cline block phase). */
  status?: string;
  pruned_models?: string[];
  skip_reason?: string;
  skip_cycle?: number;
  skips?: Record<string, string>;
}

export interface ModelProbeStatusResponse {
  enabled?: boolean;
  interval_seconds?: number;
  max_parallel?: number;
  supported_drivers?: string[];
  credentials?: ModelProbeCredentialRow[];
  next_run_at?: string;
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

export interface ModelTestResponse {
  ok?: boolean;
  model?: string;
  status?: string;
  latency_ms?: number;
  reply_text?: string;
  error?: string;
  usage?: { input?: number; output?: number };
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

  testModel: (authFile: string, model: string, signal?: AbortSignal): Promise<ModelTestResponse> =>
    apiClient.post<ModelTestResponse>(
      '/auth-files/test-model',
      { auth_file: authFile, model },
      signal ? { signal } : undefined
    ),
};
