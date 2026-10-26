import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { AuthFileItem } from '@/types';
import styles from '../components/AuthFileCard.module.scss';
import { modelProbeApi } from './api';
import { PROBE_SUPPORTED_PROVIDERS } from './logic';

export interface AuthFileProbeSectionProps {
  file: AuthFileItem;
  row: import('./logic').ProbeRowByFile[string] | undefined;
  /** Jitter-aware next scheduled cycle instant (RFC3339) from the status endpoint. */
  nextRunAt?: string;
  onProbed: (
    file: string,
    summary: {
      probed?: boolean;
      checked_at?: string;
      usable?: number;
      pruned?: number;
      pruned_models?: string[];
    }
  ) => void;
}

// Keep the card action compact; detailed probe results belong in the probe view.
export function AuthFileProbeSection({ file, onProbed }: AuthFileProbeSectionProps) {
  const { t } = useTranslation();
  const [probing, setProbing] = useState(false);
  const [error, setError] = useState('');

  const providerKey = String(file.type ?? file.provider ?? '')
    .trim()
    .toLowerCase();
  if (!PROBE_SUPPORTED_PROVIDERS.has(providerKey)) return null;
  const authIndex = typeof file.authIndex === 'string' ? file.authIndex.trim() : '';
  if (!authIndex) return null;

  const handleProbe = async () => {
    setProbing(true);
    setError('');
    try {
      const resp = await modelProbeApi.run(authIndex, true);
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
    <div className={styles.probeMeta}>
      <button
        type="button"
        className={styles.probeRun}
        disabled={probing}
        onClick={() => void handleProbe()}
      >
        {probing ? '…' : t('auth_files.probe_run')}
      </button>
      {error && (
        <span className={styles.probeError}>
          {t('auth_files.probe_failed', { message: error })}
        </span>
      )}
    </div>
  );
}
