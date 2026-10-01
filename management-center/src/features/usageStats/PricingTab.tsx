import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { IconCheck, IconPencil, IconRefreshCw, IconTrash2, IconX } from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import { usageStatsApi, type PricingEntry } from './api';
import { filterPricingEntries, formatPrice, isValidPriceInput } from './eventsLogic';
import styles from './UsageStatsPage.module.scss';

/** 定价 tab: searchable merged price table with inline edit, reset and LiteLLM sync. */
export function PricingTab() {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [entries, setEntries] = useState<PricingEntry[]>([]);
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [syncing, setSyncing] = useState(false);
  const [editing, setEditing] = useState<{ model: string; input: string; output: string } | null>(null);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const resp = await usageStatsApi.pricing();
      setEntries(resp.entries ?? []);
    } catch (err) {
      setEntries([]);
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const visible = filterPricingEntries(entries, search);

  const saveEdit = async () => {
    if (!editing || !isValidPriceInput(editing.input, editing.output)) return;
    setSaving(true);
    try {
      await usageStatsApi.putPrice(editing.model, Number(editing.input), Number(editing.output));
      showNotification(t('usage_stats.pricing_saved', { model: editing.model }), 'success');
      setEditing(null);
      await load();
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    } finally {
      setSaving(false);
    }
  };

  const resetPrice = async (model: string) => {
    try {
      await usageStatsApi.deletePrice(model);
      showNotification(t('usage_stats.pricing_reset', { model }), 'success');
      await load();
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    }
  };

  const sync = async () => {
    setSyncing(true);
    try {
      const result = await usageStatsApi.syncLitellm();
      showNotification(
        t('usage_stats.pricing_sync_result', {
          synced: result.synced,
          skipped: result.skipped_user,
        }),
        'success'
      );
      await load();
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    } finally {
      setSyncing(false);
    }
  };

  return (
    <Card className={styles.tableCard}>
      <div className={styles.filterRow}>
        <input
          className={styles.filterInput}
          value={search}
          placeholder={t('usage_stats.pricing_search')}
          aria-label={t('usage_stats.pricing_search')}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Button variant="secondary" size="sm" onClick={() => void load()} loading={loading}>
          <IconRefreshCw size={14} />
          {t('usage_stats.refresh')}
        </Button>
        <Button size="sm" onClick={() => void sync()} loading={syncing} disabled={loading}>
          {t('usage_stats.pricing_sync_button')}
        </Button>
      </div>

      {error ? (
        <div className={styles.error}>{error}</div>
      ) : visible.length === 0 ? (
        <EmptyState
          title={t('usage_stats.pricing_empty_title')}
          description={t('usage_stats.pricing_empty_description')}
        />
      ) : (
        <div className={styles.tableScroll}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>{t('usage_stats.pricing_col_model')}</th>
                <th className={styles.num}>{t('usage_stats.pricing_col_input')}</th>
                <th className={styles.num}>{t('usage_stats.pricing_col_output')}</th>
                <th>{t('usage_stats.pricing_col_source')}</th>
                <th className={styles.num}>{t('usage_stats.pricing_col_actions')}</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((entry) => {
                const isEditing = editing?.model === entry.model;
                return (
                  <tr key={entry.model}>
                    <td className={styles.keyCell}>{entry.model}</td>
                    {isEditing ? (
                      <>
                        <td className={styles.num}>
                          <input
                            className={styles.priceInput}
                            value={editing.input}
                            aria-label={t('usage_stats.pricing_col_input')}
                            onChange={(e) => setEditing({ ...editing, input: e.target.value })}
                          />
                        </td>
                        <td className={styles.num}>
                          <input
                            className={styles.priceInput}
                            value={editing.output}
                            aria-label={t('usage_stats.pricing_col_output')}
                            onChange={(e) => setEditing({ ...editing, output: e.target.value })}
                          />
                        </td>
                      </>
                    ) : (
                      <>
                        <td className={styles.num}>{formatPrice(entry.input)}</td>
                        <td className={styles.num}>{formatPrice(entry.output)}</td>
                      </>
                    )}
                    <td>
                      <span
                        className={`${styles.sourceChip} ${
                          entry.source === 'user'
                            ? styles.sourceUser
                            : entry.source === 'litellm'
                              ? styles.sourceLitellm
                              : styles.sourceDefault
                        }`}
                      >
                        {t(`usage_stats.pricing_source_${entry.source === 'user' || entry.source === 'litellm' ? entry.source : 'default'}`)}
                      </span>
                    </td>
                    <td className={styles.num}>
                      {isEditing ? (
                        <span className={styles.rowActions}>
                          <button
                            type="button"
                            className={styles.iconAction}
                            aria-label={t('usage_stats.pricing_save')}
                            title={t('usage_stats.pricing_save')}
                            disabled={saving || !isValidPriceInput(editing.input, editing.output)}
                            onClick={() => void saveEdit()}
                          >
                            <IconCheck size={14} />
                          </button>
                          <button
                            type="button"
                            className={styles.iconAction}
                            aria-label={t('usage_stats.pricing_cancel')}
                            title={t('usage_stats.pricing_cancel')}
                            disabled={saving}
                            onClick={() => setEditing(null)}
                          >
                            <IconX size={14} />
                          </button>
                        </span>
                      ) : (
                        <span className={styles.rowActions}>
                          <button
                            type="button"
                            className={styles.iconAction}
                            aria-label={t('usage_stats.pricing_edit')}
                            title={t('usage_stats.pricing_edit')}
                            onClick={() =>
                              setEditing({
                                model: entry.model,
                                input: String(entry.input),
                                output: String(entry.output),
                              })
                            }
                          >
                            <IconPencil size={14} />
                          </button>
                          {entry.source !== 'default' && (
                            <button
                              type="button"
                              className={`${styles.iconAction} ${styles.iconActionDanger}`}
                              aria-label={t('usage_stats.pricing_delete')}
                              title={t('usage_stats.pricing_delete')}
                              onClick={() => void resetPrice(entry.model)}
                            >
                              <IconTrash2 size={14} />
                            </button>
                          )}
                        </span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
