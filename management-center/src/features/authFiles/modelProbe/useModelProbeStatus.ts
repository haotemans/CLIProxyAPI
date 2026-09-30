import { useCallback, useEffect, useState } from 'react';
import type { AuthFileItem } from '@/types';
import { useAuthStore } from '@/stores';
import { modelProbeApi } from './api';
import { mergeProbeSummary, rowsToMap, type ProbeRowByFile } from './logic';

interface UseModelProbeStatusResult {
  rowsByFile: ProbeRowByFile;
  enabled: boolean;
  supportedDrivers: string[];
  updateRow: (file: string, summary: {
    probed?: boolean;
    checked_at?: string;
    usable?: number;
    pruned?: number;
    pruned_models?: string[];
  }) => void;
}

/**
 * Page-level model-probe summary: one GET /model-probe/status on mount when
 * any listed credential has a probe driver, merged afterwards per completed
 * inline probe.
 */
export function useModelProbeStatus(files: AuthFileItem[]): UseModelProbeStatusResult {
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const [rowsByFile, setRowsByFile] = useState<ProbeRowByFile>({});
  const [enabled, setEnabled] = useState(false);
  const [supportedDrivers, setSupportedDrivers] = useState<string[]>([]);

  useEffect(() => {
    if (connectionStatus !== 'connected' || files.length === 0) {
      return;
    }
    const controller = new AbortController();
    let cancelled = false;
    const load = async () => {
      try {
        const response = await modelProbeApi.status(controller.signal);
        if (cancelled) return;
        setRowsByFile(rowsToMap(response.credentials ?? []));
        setEnabled(Boolean(response.enabled));
        setSupportedDrivers(response.supported_drivers ?? []);
      } catch {
        if (!cancelled) {
          setRowsByFile({});
        }
      }
    };
    void load();
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [connectionStatus, files.length]);

  const updateRow = useCallback(
    (file: string, summary: {
      probed?: boolean;
      checked_at?: string;
      usable?: number;
      pruned?: number;
      pruned_models?: string[];
    }) => {
      setRowsByFile((previous) =>
        mergeProbeSummary(previous, file, {
          probed: summary.probed ?? true,
          checked_at: summary.checked_at ?? '',
          usable: summary.usable ?? 0,
          pruned: summary.pruned ?? 0,
          pruned_models: summary.pruned_models,
        })
      );
    },
    []
  );

  return { rowsByFile, enabled, supportedDrivers, updateRow };
}
