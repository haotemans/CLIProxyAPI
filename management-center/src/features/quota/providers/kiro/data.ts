import { createNativeQuotaLane } from '../native/data';

export const KIRO_CONFIG = createNativeQuotaLane({
  type: 'kiro',
  provider: 'kiro',
  storeKey: 'kiroQuota',
  storeSetterKey: 'setKiroQuota',
  i18nPrefix: 'kiro_quota',
});
