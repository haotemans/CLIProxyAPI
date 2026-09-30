import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import type { AuthFileItem } from '@/types';
import { modelProbeApi } from './api';
import { formatCheckedAt, PROBE_SUPPORTED_PROVIDERS, type ProbeRowByFile } from './logic';

export interface AuthFileProbeSectionProps {
  file: AuthFileItem;
  row: ProbeRowByFile[string] | undefined;
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
 * time) and offers an inline re-probe POST against the management API.
 */
export function AuthFileProbeSection({ file, row, onProbed }: AuthFileProbeSectionProps) {
  const { t } = useTranslation();
  const [probing, setProbing] = useState(false);
  const [error, setError] = useState('');

  const providerKey = String(file.type ?? file.provider ?? '').trim().toLowerCase();
  if (!PROBE_SUPPORTED_PROVIDERS.has(providerKey)) return null;
  const authIndex = typeof file.authIndex === 'string' ? file.authIndex.trim() : '';
  if (!authIndex) return null;

  const probed = Boolean(row?.probed);
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
            : t('auth_files.probe_never')}
        </span>
        <Button variant="secondary" size="sm" onClick={() => void handleProbe()} loading={probing}>
          {t('auth_files.probe_run')}
        </Button>
      </div>
      {error && <div className="auth-file-probe-error">{t('auth_files.probe_failed', { message: error })}</div>}
      {probed && (row?.pruned ?? 0) > 0 && row?.pruned_models?.length ? (
        <div className="auth-file-probe-line auth-file-probe-pruned">
          {t('auth_files.probe_pruned_detail', { models: row.pruned_models.join(', ') })}
        </div>
      ) : null}
    </div>
  );
}
