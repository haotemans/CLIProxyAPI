import { createNativeQuotaLane } from '../native/data';

export const MIRASIM_CONFIG = createNativeQuotaLane({
  type: 'mirasim',
  provider: 'mirasim',
  storeKey: 'mirasimQuota',
  storeSetterKey: 'setMirasimQuota',
  i18nPrefix: 'mirasim_quota',
});
