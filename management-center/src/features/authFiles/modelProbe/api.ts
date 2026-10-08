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
  /** Present when the run was requested with prune_unused (aggressive prune):
   * every non-usable model left the credential's effective catalog. */
  prune_run?: { at?: string; removed?: string[] };
}

export interface ModelTestResponse {
  ok?: boolean;
  model?: string;
  status?: string;
  latency_ms?: number;
  reply_text?: string;
  error?: string;
  /** /model-test(oauth probe) 专用：业务侧探测结果的状态词 + 检测时刻。 */
  provider?: string;
  checked_ms?: number;
  usage?: { input?: number; output?: number };
}

export const modelProbeApi = {
  status: (signal?: AbortSignal): Promise<ModelProbeStatusResponse> =>
    apiClient.get<ModelProbeStatusResponse>('/model-probe/status', {
      ...(signal ? { signal } : {}),
    }),

  run: (authIndex: string, pruneUnused?: boolean, signal?: AbortSignal): Promise<ModelProbeRunResponse> =>
    apiClient.post<ModelProbeRunResponse>(
      '/model-probe/run',
      { auth_index: authIndex, ...(pruneUnused ? { prune_unused: true } : {}) },
      signal ? { signal } : undefined
    ),

  testModel: (authFile: string, model: string, signal?: AbortSignal): Promise<ModelTestResponse> =>
    apiClient.post<ModelTestResponse>(
      '/auth-files/test-model',
      { auth_file: authFile, model },
      signal ? { signal } : undefined
    ),

  /** 按 auth_index 调用后端探测端点：返回统一状态词（usable/not_available/...），
   *  供凭证卡模型行就地展示「单模型测试」结果。 */
  testModelByIndex: (authIndex: string, model: string, signal?: AbortSignal): Promise<ModelTestResponse> =>
    apiClient.post<ModelTestResponse>(
      '/model-test',
      { auth_index: authIndex, model },
      signal ? { signal } : undefined
    ),
};
