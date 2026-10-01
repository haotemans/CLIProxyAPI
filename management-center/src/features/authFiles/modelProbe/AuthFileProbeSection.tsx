import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { AuthFileItem } from '@/types';
import { useNow } from '@/hooks/useNow';
import styles from '../components/AuthFileCard.module.scss';
import { modelProbeApi } from './api';
import {
  formatCheckedAt,
  formatRelativeFromNow,
  PROBE_SUPPORTED_PROVIDERS,
  splitProbeSummary,
  type ProbeSummaryPart,
} from './logic';

export interface AuthFileProbeSectionProps {
  file: AuthFileItem;
  row: import('./logic').ProbeRowByFile[string] | undefined;
  /** Jitter-aware next scheduled cycle instant (RFC3339) from the status endpoint. */
  nextRunAt?: string;
  onProbed: (file: string, summary: {
    probed?: boolean;
    checked_at?: string;
    usable?: number;
    pruned?: number;
    pruned_models?: string[];
  }) => void;
}

const partClass = (part: ProbeSummaryPart, usable: number, pruned: number): string => {
  switch (part.kind) {
    case 'usable':
      return usable > 0 ? styles.probeCountUsable : styles.probeCountNeutral;
    case 'pruned':
      return pruned > 0 ? styles.probeCountPruned : styles.probeCountNeutral;
    default:
      return styles.probeCountNeutral;
  }
};

/**
 * Per-card model-probe line: one small muted meta row (same visual weight as
 * the other card meta lines) summarizing usable/pruned counts and last check
 * time, with a subtle inline re-probe action. Status color shows only on
 * non-zero counts, keeping the block quiet until it matters.
 */
export function AuthFileProbeSection({ file, row, nextRunAt, onProbed }: AuthFileProbeSectionProps) {
  const { t } = useTranslation();
  const now = useNow();
  const [probing, setProbing] = useState(false);
  const [error, setError] = useState('');

  const providerKey = String(file.type ?? file.provider ?? '').trim().toLowerCase();
  if (!PROBE_SUPPORTED_PROVIDERS.has(providerKey)) return null;
  const authIndex = typeof file.authIndex === 'string' ? file.authIndex.trim() : '';
  if (!authIndex) return null;

  const probed = Boolean(row?.probed);
  const skipReason = row?.skip_reason?.trim();
  const usable = row?.usable ?? 0;
  const pruned = row?.pruned ?? 0;

  const handleProbe = async () => {
    setProbing(true);
    setError('');
    try {
      const resp = await modelProbeApi.run(authIndex);
      if (resp.summary) {
        onProbed(file.name, {
          probed: resp.summary.probed,
          checked_at: resp.summary.checked_at,
          usable: resp.summary.usable,
          pruned: resp.summary.pruned,
          pruned_models: resp.summary.pruned_models,
        });
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setProbing(false);
    }
  };

  const renderProbeSentence = (values: { usable: string; pruned: string; date: string }) =>
    t('auth_files.probe_summary', {
      usable: values.usable,
      pruned: values.pruned,
      checked_at: values.date,
    });

  const parts = probed
    ? splitProbeSummary(renderProbeSentence, usable, pruned, formatCheckedAt(row?.checked_at))
    : [];
  const nextRunLabel = !probed && nextRunAt ? formatRelativeFromNow(nextRunAt, now) : '';

  return (
    <div className={styles.probeMeta}>
      <span className={styles.probeDot} aria-hidden="true">
        {probed ? '✓' : '○'}
      </span>
      {probed ? (
        <span className={styles.probeTitle}>
          {parts.map((part, index) =>
            part.kind === 'text' ? (
              <span key={index}>{part.text}</span>
            ) : (
              <span key={index} className={partClass(part, usable, pruned)}>
                {part.text}
              </span>
            )
          )}
        </span>
      ) : (
        <span className={styles.probeTitleMuted}>
          {nextRunLabel
            ? t('auth_files.probe_never_next', { in: nextRunLabel })
            : t('auth_files.probe_never')}
        </span>
      )}
      <button
        type="button"
        className={styles.probeRun}
        disabled={probing}
        onClick={() => void handleProbe()}
      >
        {probing ? '…' : t('auth_files.probe_run')}
      </button>
      {error && (
        <span className={styles.probeError}>{t('auth_files.probe_failed', { message: error })}</span>
      )}
      {skipReason && (
        <span className={styles.probeNoteMuted}>{t('auth_files.probe_skipped', { reason: skipReason })}</span>
      )}
      {probed && pruned > 0 && row?.pruned_models?.length ? (
        <span className={styles.probeNoteMuted}>
          {t('auth_files.probe_pruned_detail', { models: row.pruned_models.join(', ') })}
        </span>
      ) : null}
    </div>
  );
}
