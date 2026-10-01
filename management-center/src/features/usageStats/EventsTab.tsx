import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { IconRefreshCw, IconX } from '@/components/ui/icons';
import { usageStatsApi, type UsageEventsResponse } from './api';
import { effectiveCostUSD, formatTokens, formatUSD } from './logic';
import {
  clampEventPage,
  defaultEventFilter,
  formatEventTs,
  formatLatency,
  totalEventPages,
  type UsageEventFilter,
  type UsageEventRow,
} from './eventsLogic';
import styles from './UsageStatsPage.module.scss';

interface EventsTabProps {
  /** Window from the page-level preset/custom range. */
  window: { from: number; to: number };
}

/** 请求记录 tab: filterable, paginated per-request archive with a detail drawer. */
export function EventsTab({ window }: EventsTabProps) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState<UsageEventFilter>(() =>
    defaultEventFilter(window.from, window.to)
  );
  const [data, setData] = useState<UsageEventsResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState<UsageEventRow | null>(null);
  const requestId = useRef(0);

  // The page-level range resets the listing window + page.
  useEffect(() => {
    setFilter((prev) => ({ ...prev, from: window.from, to: window.to, page: 1 }));
  }, [window.from, window.to]);

  const load = useCallback(async () => {
    const request = ++requestId.current;
    setLoading(true);
    setError('');
    try {
      const resp = await usageStatsApi.events(filter);
      if (request !== requestId.current) return;
      setData(resp);
    } catch (err) {
      if (request !== requestId.current) return;
      setData(null);
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (request === requestId.current) setLoading(false);
    }
  }, [filter]);

  useEffect(() => {
    void load();
  }, [load]);

  const patch = (partial: Partial<UsageEventFilter>) =>
    setFilter((prev) => ({ ...prev, page: 1, ...partial }));

  const rows = data?.rows ?? [];
  const total = data?.total ?? 0;
  const pages = totalEventPages(total, filter.pageSize);
  const page = clampEventPage(filter.page, pages);

  const enabled = data?.enabled !== false;
  const showEmpty = !loading && !error && rows.length === 0;

  return (
    <Card className={styles.tableCard}>
      <div className={styles.filterRow}>
        <input
          className={styles.filterInput}
          value={filter.provider}
          placeholder={t('usage_stats.events_filter_provider')}
          aria-label={t('usage_stats.events_filter_provider')}
          onChange={(e) => patch({ provider: e.target.value })}
        />
        <input
          className={styles.filterInput}
          value={filter.model}
          placeholder={t('usage_stats.events_filter_model')}
          aria-label={t('usage_stats.events_filter_model')}
          onChange={(e) => patch({ model: e.target.value })}
        />
        <input
          className={styles.filterInput}
          value={filter.authFile}
          placeholder={t('usage_stats.events_filter_auth')}
          aria-label={t('usage_stats.events_filter_auth')}
          onChange={(e) => patch({ authFile: e.target.value })}
        />
        <div className={styles.presetGroup}>
          {(['', 'ok', 'error'] as const).map((value) => (
            <button
              key={value || 'all'}
              type="button"
              className={`${styles.presetButton} ${filter.status === value ? styles.presetButtonActive : ''}`}
              onClick={() => patch({ status: value })}
            >
              {value === '' ? t('usage_stats.events_status_all') : t(`usage_stats.events_status_${value}`)}
            </button>
          ))}
        </div>
        <Button variant="secondary" size="sm" onClick={() => void load()} loading={loading}>
          <IconRefreshCw size={14} />
          {t('usage_stats.refresh')}
        </Button>
      </div>

      {!enabled && <div className={styles.hint}>{t('usage_stats.disabled_hint')}</div>}
      {error ? (
        <div className={styles.error}>{error}</div>
      ) : showEmpty ? (
        <EmptyState
          title={t('usage_stats.events_empty_title')}
          description={t('usage_stats.events_empty_description')}
        />
      ) : (
        <div className={styles.tableScroll}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>{t('usage_stats.events_col_ts')}</th>
                <th>{t('usage_stats.events_col_provider')}</th>
                <th>{t('usage_stats.events_col_model')}</th>
                <th>{t('usage_stats.events_col_auth')}</th>
                <th className={styles.num}>{t('usage_stats.events_col_tokens')}</th>
                <th className={styles.num}>{t('usage_stats.events_col_cost')}</th>
                <th>{t('usage_stats.events_col_status')}</th>
                <th className={styles.num}>{t('usage_stats.events_col_latency')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr
                  key={row.id}
                  className={styles.clickableRow}
                  onClick={() => setSelected(row)}
                >
                  <td className={styles.monoCell}>{formatEventTs(row.ts)}</td>
                  <td>{row.provider || '-'}</td>
                  <td className={styles.keyCell} title={row.upstream_model !== row.model ? row.upstream_model : undefined}>
                    {row.model || '-'}
                  </td>
                  <td className={styles.keyCell} title={row.auth_file}>
                    {row.auth_file || <span className={styles.keyEmpty}>(empty)</span>}
                  </td>
                  <td className={styles.num}>
                    {formatTokens(row.input_tokens)}→{formatTokens(row.output_tokens)}
                  </td>
                  <td className={styles.num}>
                    {formatUSD(effectiveCostUSD(row.upstream_cost_usd, row.computed_cost_usd))}
                  </td>
                  <td>
                    <span className={row.status === 'ok' ? styles.statusOk : styles.statusError}>
                      {row.status === 'ok'
                        ? t('usage_stats.events_status_ok')
                        : row.error || t('usage_stats.events_status_error')}
                    </span>
                  </td>
                  <td className={styles.num}>{formatLatency(row.latency_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className={styles.pager}>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setFilter((prev) => ({ ...prev, page: prev.page - 1 }))}
          disabled={page <= 1 || loading}
        >
          {t('usage_stats.events_prev')}
        </Button>
        <span className={styles.pagerInfo}>
          {t('usage_stats.events_page_info', { page, pages, total })}
        </span>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setFilter((prev) => ({ ...prev, page: prev.page + 1 }))}
          disabled={page >= pages || loading}
        >
          {t('usage_stats.events_next')}
        </Button>
      </div>

      {selected && (
        <div className={styles.drawerMask} onClick={() => setSelected(null)}>
          <aside className={styles.drawer} onClick={(e) => e.stopPropagation()}>
            <header className={styles.drawerHead}>
              <h3>{t('usage_stats.events_detail_title')}</h3>
              <button
                type="button"
                className={styles.drawerClose}
                aria-label={t('usage_stats.events_detail_close')}
                onClick={() => setSelected(null)}
              >
                <IconX size={16} />
              </button>
            </header>
            <dl className={styles.drawerBody}>
              <DetailRow label={t('usage_stats.events_col_ts')} value={formatEventTs(selected.ts)} />
              <DetailRow label={t('usage_stats.events_col_provider')} value={selected.provider} />
              <DetailRow label={t('usage_stats.events_col_model')} value={selected.model} />
              <DetailRow label={t('usage_stats.events_detail_upstream_model')} value={selected.upstream_model} />
              <DetailRow label={t('usage_stats.events_col_auth')} value={selected.auth_file} />
              <DetailRow label={t('usage_stats.events_detail_api_key')} value={selected.api_key || '-'} />
              <DetailRow label={t('usage_stats.events_detail_endpoint')} value={selected.endpoint || '-'} />
              <DetailRow label={t('usage_stats.events_detail_input')} value={selected.input_tokens.toLocaleString()} />
              <DetailRow label={t('usage_stats.events_detail_output')} value={selected.output_tokens.toLocaleString()} />
              <DetailRow label={t('usage_stats.events_detail_cached')} value={selected.cached_tokens.toLocaleString()} />
              <DetailRow label={t('usage_stats.events_detail_total')} value={selected.total_tokens.toLocaleString()} />
              <DetailRow
                label={t('usage_stats.events_col_cost')}
                value={`${formatUSD(selected.upstream_cost_usd)} / ${formatUSD(selected.computed_cost_usd)}`}
              />
              <DetailRow
                label={t('usage_stats.events_col_status')}
                value={selected.status + (selected.error ? ` (${selected.error})` : '')}
              />
              <DetailRow label={t('usage_stats.events_col_latency')} value={formatLatency(selected.latency_ms)} />
            </dl>
          </aside>
        </div>
      )}
    </Card>
  );
}

function DetailRow({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt>{label}</dt>
      <dd className={styles.drawerValue}>{value}</dd>
    </>
  );
}
