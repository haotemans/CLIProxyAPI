import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { IconRefreshCw } from '@/components/ui/icons';
import { usageStatsApi, type UsageSummaryResponse } from './api';
import {
  effectiveCostUSD,
  formatTokens,
  formatUSD,
  paletteForKey,
  presetRangeWindow,
  shapeSeriesByDay,
  sortSummaryDesc,
  type UsageRangePreset,
  type UsageSeriesRow,
  type UsageWindow,
} from './logic';
import styles from './UsageStatsPage.module.scss';

type GroupTab = 'provider' | 'model' | 'auth_file';

const GROUP_TABS: GroupTab[] = ['provider', 'model', 'auth_file'];

const PRESETS: UsageRangePreset[] = ['24h', '7d', '30d', 'custom'];

const fromLocalInput = (value: string): number | null => {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
};

export function UsageStatsPage() {
  const { t } = useTranslation();
  const [preset, setPreset] = useState<UsageRangePreset>('7d');
  const [customFrom, setCustomFrom] = useState('');
  const [customTo, setCustomTo] = useState('');
  const [groupBy, setGroupBy] = useState<GroupTab>('provider');
  const [summary, setSummary] = useState<UsageSummaryResponse | null>(null);
  const [seriesRows, setSeriesRows] = useState<UsageSeriesRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const requestId = useRef(0);

  const window = useMemo<UsageWindow>(() => {
    const now = Date.now();
    if (preset === 'custom') {
      const from = fromLocalInput(customFrom);
      const to = fromLocalInput(customTo);
      if (from != null && to != null && from <= to) return { from, to };
    }
    return presetRangeWindow(preset, now);
  }, [preset, customFrom, customTo]);

  const load = useCallback(async () => {
    const request = ++requestId.current;
    setLoading(true);
    setError('');
    try {
      const [summaryResp, seriesResp] = await Promise.all([
        usageStatsApi.summary({ from: window.from, to: window.to, group_by: groupBy }),
        usageStatsApi.series({ from: window.from, to: window.to, interval: 'day', group_by: 'provider' }),
      ]);
      if (request !== requestId.current) return;
      setSummary(summaryResp);
      setSeriesRows(seriesResp.rows ?? []);
    } catch (err) {
      if (request !== requestId.current) return;
      setSummary(null);
      setSeriesRows([]);
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (request === requestId.current) setLoading(false);
    }
  }, [window.from, window.to, groupBy]);

  // Auto-refresh is intentionally off by default; the refresh button reloads.
  useEffect(() => {
    void load();
  }, [load]);

  const chart = useMemo(
    () => shapeSeriesByDay(seriesRows, window.from, window.to),
    [seriesRows, window.from, window.to]
  );

  const rows = useMemo(
    () => (summary?.rows ? sortSummaryDesc(summary.rows) : []),
    [summary?.rows]
  );

  const totals = summary?.totals;
  const showEmpty =
    !loading && !error && rows.length === 0 && chart.days.every((d) => d.total === 0);

  return (
    <div className={styles.container}>
      <section className={styles.header}>
        <h1 className={styles.title}>{t('usage_stats.title')}</h1>
        <div className={styles.headerActions}>
          <div className={styles.presetGroup}>
            {PRESETS.map((item) => (
              <button
                key={item}
                type="button"
                className={`${styles.presetButton} ${preset === item ? styles.presetButtonActive : ''}`}
                onClick={() => setPreset(item)}
              >
                {t(`usage_stats.range_${item}`)}
              </button>
            ))}
          </div>
          {preset === 'custom' && (
            <div className={styles.customRange}>
              <input
                type="datetime-local"
                aria-label={t('usage_stats.custom_from')}
                value={customFrom}
                onChange={(e) => setCustomFrom(e.target.value)}
              />
              <span className={styles.rangeSep}>→</span>
              <input
                type="datetime-local"
                aria-label={t('usage_stats.custom_to')}
                value={customTo}
                onChange={(e) => setCustomTo(e.target.value)}
              />
            </div>
          )}
          <Button variant="secondary" size="sm" onClick={() => void load()} loading={loading}>
            <IconRefreshCw size={14} />
            {t('usage_stats.refresh')}
          </Button>
        </div>
      </section>

      {summary && !summary.enabled && (
        <div className={styles.hint}>{t('usage_stats.disabled_hint')}</div>
      )}

      <section className={styles.cards}>
        {loading ? (
          <>
            <Card className={styles.statCard}><Skeleton width="70%" /><Skeleton width="40%" /></Card>
            <Card className={styles.statCard}><Skeleton width="70%" /><Skeleton width="40%" /></Card>
            <Card className={styles.statCard}><Skeleton width="70%" /><Skeleton width="40%" /></Card>
            <Card className={styles.statCard}><Skeleton width="70%" /><Skeleton width="40%" /></Card>
          </>
        ) : totals ? (
          <>
            <Card className={styles.statCard} title={t('usage_stats.card_requests')}>
              <div className={styles.statValue}>{totals.requests.toLocaleString()}</div>
              <div className={styles.statSub}>{t('usage_stats.card_tokens_hint', { count: formatTokens(totals.total_tokens) })}</div>
            </Card>
            <Card className={styles.statCard} title={t('usage_stats.card_input')}>
              <div className={styles.statValue}>{formatTokens(totals.input_tokens)}</div>
            </Card>
            <Card className={styles.statCard} title={t('usage_stats.card_output')}>
              <div className={styles.statValue}>{formatTokens(totals.output_tokens)}</div>
            </Card>
            <Card className={styles.statCard} title={t('usage_stats.card_cost')}>
              <div className={styles.statValue}>
                {formatUSD(effectiveCostUSD(totals.upstream_cost_usd, totals.computed_cost_usd))}
              </div>
              <div className={styles.statSub}>
                {t('usage_stats.cost_split', {
                  upstream: formatUSD(totals.upstream_cost_usd),
                  computed: formatUSD(totals.computed_cost_usd),
                })}
              </div>
            </Card>
          </>
        ) : null}
      </section>

      <Card
        className={styles.chartCard}
        title={
          <div className={styles.chartHeader}>
            <span>{t('usage_stats.chart_title')}</span>
            <div className={styles.legend}>
              {chart.keys.map((key, index) => (
                <span key={key} className={styles.legendItem}>
                  <span className={styles.legendSwatch} style={{ background: paletteForKey(key, index) }} />
                  {key}
                </span>
              ))}
            </div>
          </div>
        }
      >
        {loading ? (
          <Skeleton width="100%" height={120} />
        ) : chart.maxTotal > 0 ? (
          <div className={styles.chart} role="img" aria-label={t('usage_stats.chart_title')}>
            {chart.days.map((day) => (
              <div key={day.bucket} className={styles.chartColumn}>
                <div className={styles.chartBars}>
                  {day.segments.map((segment) => (
                    <div
                      key={segment.key}
                      className={styles.chartSegment}
                      title={`${day.bucket} ${segment.key}: ${formatUSD(segment.value)}`}
                      style={{
                        height: `${(segment.value / chart.maxTotal) * 100}%`,
                        background: paletteForKey(segment.key, chart.keys.indexOf(segment.key)),
                      }}
                    />
                  ))}
                </div>
                <span className={styles.chartLabel}>{day.bucket.slice(5)}</span>
              </div>
            ))}
          </div>
        ) : (
          <div className={styles.emptyChart}>{t('usage_stats.empty_chart')}</div>
        )}
      </Card>

      <Card
        className={styles.tableCard}
        title={
          <div className={styles.tableHeader}>
            <span>{t('usage_stats.table_title')}</span>
            <div className={styles.groupTabs}>
              {GROUP_TABS.map((tab) => (
                <button
                  key={tab}
                  type="button"
                  className={`${styles.presetButton} ${groupBy === tab ? styles.presetButtonActive : ''}`}
                  onClick={() => setGroupBy(tab)}
                >
                  {t(`usage_stats.group_${tab}`)}
                </button>
              ))}
            </div>
          </div>
        }
      >
        {loading ? (
          <Skeleton width="100%" height={180} />
        ) : error ? (
          <div className={styles.error}>{error}</div>
        ) : showEmpty ? (
          <EmptyState
            title={t('usage_stats.empty_title')}
            description={t('usage_stats.empty_description')}
          />
        ) : (
          <div className={styles.tableScroll}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('usage_stats.col_key')}</th>
                  <th className={styles.num}>{t('usage_stats.col_requests')}</th>
                  <th className={styles.num}>{t('usage_stats.col_input')}</th>
                  <th className={styles.num}>{t('usage_stats.col_output')}</th>
                  <th className={styles.num}>{t('usage_stats.col_cost')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.key}>
                    <td className={styles.keyCell}>
                      {row.key || <span className={styles.keyEmpty}>(empty)</span>}
                    </td>
                    <td className={styles.num}>{row.requests.toLocaleString()}</td>
                    <td className={styles.num}>{formatTokens(row.input_tokens)}</td>
                    <td className={styles.num}>{formatTokens(row.output_tokens)}</td>
                    <td className={styles.num}>
                      {formatUSD(effectiveCostUSD(row.upstream_cost_usd, row.computed_cost_usd))}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
