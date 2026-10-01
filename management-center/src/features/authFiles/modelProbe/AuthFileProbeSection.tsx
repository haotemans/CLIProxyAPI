import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import type { AuthFileItem } from '@/types';
import { useNow } from '@/hooks/useNow';
import { modelProbeApi } from './api';
import {
  formatCheckedAt,
  formatRelativeFromNow,
  PROBE_SUPPORTED_PROVIDERS,
  type ProbeRowByFile,
} from './logic';

export interface AuthFileProbeSectionProps {
  file: AuthFileItem;
  row: ProbeRowByFile[string] | undefined;
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

/**
 * Per-card model-probe footer: shows whether the backend verified each
 * advertised model for this credential (✓ usable / ✗ pruned + last check
 * time), the next scheduled run, and offers an inline re-probe POST.
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

  const nextRunLabel = !probed && nextRunAt ? formatRelativeFromNow(nextRunAt, now) : '';

  return (
    <div className="auth-file-probe">
      <div className="auth-file-probe-line">
        <span className="auth-file-probe-icon" aria-hidden>
          {probed ? '✓' : '○'}
        </span>
        <span>
          {probed
            ? t('auth_files.probe_summary', {
                usable: row?.usable ?? 0,
                pruned: row?.pruned ?? 0,
                checked_at: formatCheckedAt(row?.checked_at),
              })
            : nextRunLabel
              ? t('auth_files.probe_never_next', { in: nextRunLabel })
              : t('auth_files.probe_never')}
        </span>
        <Button variant="secondary" size="sm" onClick={() => void handleProbe()} loading={probing}>
          {t('auth_files.probe_run')}
        </Button>
      </div>
      {error && <div className="auth-file-probe-error">{t('auth_files.probe_failed', { message: error })}</div>}
      {skipReason && (
        <div className="auth-file-probe-line auth-file-probe-skipped">
          {t('auth_files.probe_skipped', { reason: skipReason })}
        </div>
      )}
      {probed && (row?.pruned ?? 0) > 0 && row?.pruned_models?.length ? (
        <div className="auth-file-probe-line auth-file-probe-pruned">
          {t('auth_files.probe_pruned_detail', { models: row.pruned_models.join(', ') })}
        </div>
      ) : null}
    </div>
  );
}
