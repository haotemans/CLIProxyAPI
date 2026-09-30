import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { NativeQuotaState } from '@/types';
import { buildResetDisplay } from '@/utils/quota';
import { useNow } from '@/hooks/useNow';
import { QuotaMeter } from '../../components/QuotaMeter';
import { QuotaResetLabel } from '../../components/QuotaResetLabel';
import type { QuotaBodyProps } from '../../types';

/**
 * Generic meter-rows body for native builtin quota lanes (kiro, mirasim).
 * The i18n prefix parameter selects the provider's qualified locale keys
 * (plan label + empty-data string); row rendering is shared.
 */
export function createNativeQuotaBody(i18nPrefix: string) {
  return function NativeQuotaBody({ quota, classes }: QuotaBodyProps<NativeQuotaState>) {
    const { t, i18n } = useTranslation();
    const now = useNow();
    const rows = quota.rows ?? [];

    const resetDisplays = useMemo(
      () => rows.map((row) => buildResetDisplay(null, row.resetAtMs ?? null, now, i18n.resolvedLanguage)),
      [rows, now, i18n.resolvedLanguage]
    );

    if (rows.length === 0) {
      return (
        <>
          {quota.plan && (
            <span className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{t(`${i18nPrefix}.plan`)}</span>
              <span className={classes.codexPlanValue}>{quota.plan}</span>
            </span>
          )}
          <div className={classes.quotaMessage}>{t(`${i18nPrefix}.empty_data`)}</div>
        </>
      );
    }

    return (
      <>
        {quota.plan && (
          <span className={classes.codexPlanItem}>
            <span className={classes.codexPlanLabel}>{t(`${i18nPrefix}.plan`)}</span>
            <span className={classes.codexPlanValue}>{quota.plan}</span>
          </span>
        )}
        {rows.map((row, index) => {
          const limit = row.limit;
          const used = row.used;
          const remaining =
            limit > 0 ? Math.max(0, Math.min(100, Math.round(((limit - used) / limit) * 100))) : null;
          const percentLabel = remaining === null ? '--' : `${remaining}%`;
          return (
            <div key={row.id} className={classes.quotaRow}>
              <div className={classes.quotaRowHeader}>
                <span className={classes.quotaModel}>{row.label}</span>
                <div className={classes.quotaMeta}>
                  <span className={classes.quotaPercent}>{percentLabel}</span>
                  {resetDisplays[index] && (
                    <QuotaResetLabel display={resetDisplays[index]} classes={classes} soon={false} />
                  )}
                </div>
              </div>
              <QuotaMeter percent={remaining} classes={classes} index={index} />
            </div>
          );
        })}
      </>
    );
  };
}
