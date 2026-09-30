import type { TFunction } from 'i18next';
import type { NativeQuotaState } from '@/types';
import { isDisabledAuthFile, resolveAuthProvider } from '@/utils/quota';
import type { QuotaProviderData, QuotaProviderType, QuotaStore } from '../types';
import { fetchNativeQuota, NativeQuotaError, type NativeQuotaData } from './requests';

interface NativeLaneConfig {
  type: QuotaProviderType;
  provider: string;
  storeKey: 'kiroQuota' | 'mirasimQuota';
  storeSetterKey: 'setKiroQuota' | 'setMirasimQuota';
  i18nPrefix: string;
}

/**
 * Build a builtin quota lane over the backend's native quota fetch
 * (POST /v0/management/quota/fetch) for providers with a native quota fetcher.
 */
export function createNativeQuotaLane(
  config: NativeLaneConfig
): QuotaProviderData<NativeQuotaState, NativeQuotaData> {
  return {
    type: config.type,
    i18nPrefix: config.i18nPrefix,
    filterFn: (file) => resolveAuthProvider(file) === config.provider && !isDisabledAuthFile(file),
    fetchQuota: async (file, t: TFunction) => {
      try {
        return await fetchNativeQuota(file);
      } catch (error: unknown) {
        if (error instanceof NativeQuotaError) {
          error.message = t(`${config.i18nPrefix}.${error.code}`, { status: error.status });
        }
        throw error;
      }
    },
    storeSelector: (state: QuotaStore) => state[config.storeKey] as Record<string, NativeQuotaState>,
    storeSetter: config.storeSetterKey as keyof QuotaStore,
    buildLoadingState: () => ({ status: 'loading', rows: [] }),
    buildSuccessState: (data) => ({
      status: 'success',
      plan: data.plan,
      rows: data.rows,
    }),
    buildErrorState: (message, errorStatus) => ({
      status: 'error',
      rows: [],
      error: message,
      errorStatus,
    }),
  };
}
