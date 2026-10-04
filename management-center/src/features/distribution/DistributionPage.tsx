import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { EmptyState } from '@/components/ui/EmptyState';
import { useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import { formatTokens, formatUSD } from '@/features/usageStats/logic';
import {
  distributionKeysApi,
  type DistributionKeyRow,
  type DistributionKeyCreateResponse,
} from './api';
import {
  distributionFormFromRow,
  distributionKeyStatus,
  distributionWritePayload,
  emptyDistributionForm,
  hasQuota,
  quotaUsageRatio,
  validateDistributionForm,
  type DistributionFormValues,
} from './logic';
import styles from './DistributionPage.module.scss';

export function DistributionPage() {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);

  const [rows, setRows] = useState<DistributionKeyRow[]>([]);
  const [windowByHash, setWindowByHash] = useState<Record<string, { requests: number; computed_cost_usd: number; upstream_cost_usd: number }>>({});
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [saving, setSaving] = useState(false);

  const [modalOpen, setModalOpen] = useState(false);
  const [editingRow, setEditingRow] = useState<DistributionKeyRow | null>(null);
  const [form, setForm] = useState<DistributionFormValues>(emptyDistributionForm);
  const [formError, setFormError] = useState('');

  const [createdKey, setCreatedKey] = useState<DistributionKeyCreateResponse | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError('');
    try {
      const [list, usage] = await Promise.all([
        distributionKeysApi.list(),
        distributionKeysApi.usage().catch(() => ({ rows: [], from: 0, to: 0 })),
      ]);
      setRows(list['distribution-keys'] ?? []);
      const next: Record<string, { requests: number; computed_cost_usd: number; upstream_cost_usd: number }> = {};
      for (const row of usage.rows ?? []) {
        if (row.window) {
          next[row.key_hash] = {
            requests: row.window.requests,
            computed_cost_usd: row.window.computed_cost_usd,
            upstream_cost_usd: row.window.upstream_cost_usd,
          };
        }
      }
      setWindowByHash(next);
    } catch (err) {
      setLoadError(String(err instanceof Error ? err.message : err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const openCreate = () => {
    setEditingRow(null);
    setForm(emptyDistributionForm);
    setFormError('');
    setModalOpen(true);
  };

  const openEdit = (row: DistributionKeyRow) => {
    setEditingRow(row);
    setForm(distributionFormFromRow(row));
    setFormError('');
    setModalOpen(true);
  };

  const save = async () => {
    const invalid = validateDistributionForm(form, !editingRow);
    if (invalid) {
      setFormError(t(invalid.field === 'expiresAt' ? 'distribution.form_error_expiry' : 'distribution.form_error_quota'));
      return;
    }
    setSaving(true);
    setFormError('');
    try {
      if (editingRow) {
        await distributionKeysApi.update(editingRow.key_hash, distributionWritePayload(form, false));
        showNotification(t('distribution.updated'), 'success');
        setModalOpen(false);
      } else {
        const created = await distributionKeysApi.create(distributionWritePayload(form, true));
        setModalOpen(false);
        setCreatedKey(created);
      }
      await load();
    } catch (err) {
      setFormError(String(err instanceof Error ? err.message : err));
    } finally {
      setSaving(false);
    }
  };

  const toggleEnabled = async (row: DistributionKeyRow) => {
    try {
      await distributionKeysApi.update(row.key_hash, { enabled: !row.enabled });
      await load();
    } catch (err) {
      showNotification(String(err instanceof Error ? err.message : err), 'error');
    }
  };

  const resetUsage = async (row: DistributionKeyRow) => {
    try {
      await distributionKeysApi.resetUsage(row.key_hash);
      showNotification(t('distribution.usage_reset'), 'success');
      await load();
    } catch (err) {
      showNotification(String(err instanceof Error ? err.message : err), 'error');
    }
  };

  const removeKey = async (row: DistributionKeyRow) => {
    try {
      await distributionKeysApi.remove(row.key_hash);
      showNotification(t('distribution.deleted'), 'success');
      await load();
    } catch (err) {
      showNotification(String(err instanceof Error ? err.message : err), 'error');
    }
  };

  const copyText = async (text: string) => {
    const copied = await copyToClipboard(text);
    showNotification(
      copied
        ? t('notification.link_copied', { defaultValue: 'Copied to clipboard' })
        : t('notification.copy_failed', { defaultValue: 'Copy failed' }),
      copied ? 'success' : 'error'
    );
  };

  const empty = useMemo(() => !loading && rows.length === 0, [loading, rows.length]);

  return (
    <div className={styles.page}>
      <div className={styles.headerRow}>
        <div>
          <h1 className={styles.title}>{t('distribution.title')}</h1>
          <p className={styles.subtitle}>{t('distribution.subtitle')}</p>
        </div>
        <div className={styles.headerActions}>
          <Button variant="secondary" onClick={() => void load()} disabled={loading}>
            {t('common.refresh')}
          </Button>
          <Button variant="primary" onClick={openCreate}>
            {t('distribution.create')}
          </Button>
        </div>
      </div>

      {loadError && <div className={styles.errorText}>{loadError}</div>}

      {empty ? (
        <Card>
          <EmptyState title={t('distribution.empty_title')} description={t('distribution.empty_desc')} />
        </Card>
      ) : (
        <div className={styles.list}>
          {rows.map((row) => {
            const status = distributionKeyStatus(row);
            const ratio = quotaUsageRatio(row.quota_usd, row.spent_usd);
            const window = windowByHash[row.key_hash];
            const windowCost = window ? (window.upstream_cost_usd > 0 ? window.upstream_cost_usd : window.computed_cost_usd) : 0;
            return (
              <Card key={row.key_hash} className={styles.rowCard}>
                <div className={styles.rowHeader}>
                  <div className={styles.rowTitle}>
                    <span className={styles.keyName}>{row.name?.trim() || row.key_masked}</span>
                    <button
                      type="button"
                      className={styles.keyMasked}
                      title={row.key_hash}
                      onClick={() => void copyText(row.key_hash)}
                    >
                      {row.key_masked}
                    </button>
                    <span className={`${styles.statusChip} ${styles[`status_${status}`]}`}>
                      {t(`distribution.status_${status}`)}
                    </span>
                  </div>
                  <div className={styles.rowActions}>
                    <ToggleSwitch
                      checked={row.enabled}
                      onChange={() => void toggleEnabled(row)}
                      ariaLabel={t(`distribution.status_${status}`)}
                    />
                    <button type="button" className={styles.actionBtn} onClick={() => openEdit(row)}>
                      {t('distribution.edit')}
                    </button>
                    <button type="button" className={styles.actionBtn} onClick={() => void resetUsage(row)}>
                      {t('distribution.reset_usage')}
                    </button>
                    <button type="button" className={`${styles.actionBtn} ${styles.actionDanger}`} onClick={() => void removeKey(row)}>
                      {t('distribution.delete')}
                    </button>
                  </div>
                </div>
                <div className={styles.rowMeta}>
                  <div className={styles.quotaBlock}>
                    {hasQuota(row.quota_usd) ? (
                      <>
                        <div className={styles.quotaText}>
                          {t('distribution.quota_progress', {
                            spent: formatUSD(row.spent_usd),
                            quota: formatUSD(row.quota_usd ?? 0),
                          })}
                        </div>
                        <div className={styles.quotaBar} role="progressbar" aria-valuenow={Math.round((ratio ?? 0) * 100)} aria-valuemin={0} aria-valuemax={100}>
                          <div
                            className={`${styles.quotaFill} ${ratio !== null && ratio >= 1 ? styles.quotaFillFull : ''}`}
                            style={{ width: `${Math.round((ratio ?? 0) * 100)}%` }}
                          />
                        </div>
                      </>
                    ) : (
                      <div className={styles.quotaTextMuted}>
                        {t('distribution.quota_unlimited', { spent: formatUSD(row.spent_usd) })}
                      </div>
                    )}
                  </div>
                  <div className={styles.metaItem}>
                    {row.allowed_models && row.allowed_models.length > 0
                      ? t('distribution.models_limited', { count: row.allowed_models.length })
                      : t('distribution.models_all')}
                  </div>
                  <div className={styles.metaItem}>
                    {row.expires_at
                      ? t('distribution.expiry_at', { at: row.expires_at.replace('T', ' ').replace('Z', '') })
                      : t('distribution.expiry_never')}
                  </div>
                  <div className={styles.metaItem}>
                    {row.last_request_at
                      ? t('distribution.last_request', { at: row.last_request_at.replace('T', ' ').replace('Z', '') })
                      : t('distribution.no_traffic')}
                  </div>
                  {window && (
                    <div className={styles.metaItem}>
                      {t('distribution.window_stats', {
                        requests: formatTokens(window.requests),
                        cost: formatUSD(windowCost),
                      })}
                    </div>
                  )}
                </div>
              </Card>
            );
          })}
        </div>
      )}

      <Modal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        title={editingRow ? t('distribution.edit_title') : t('distribution.create_title')}
        footer={
          <>
            <Button variant="secondary" onClick={() => setModalOpen(false)} disabled={saving}>
              {t('common.cancel')}
            </Button>
            <Button variant="primary" onClick={() => void save()} loading={saving}>
              {t('distribution.save')}
            </Button>
          </>
        }
      >
        <div className={styles.form}>
          {!editingRow && (
            <div className={styles.formField}>
              <label className={styles.formLabel}>{t('distribution.form_key')}</label>
              <input
                className={styles.formInput}
                value={form.key}
                placeholder={t('distribution.form_key_placeholder')}
                onChange={(e) => setForm({ ...form, key: e.target.value })}
                disabled={saving}
              />
              <div className={styles.formHint}>{t('distribution.form_key_hint')}</div>
            </div>
          )}
          <div className={styles.formField}>
            <label className={styles.formLabel}>{t('distribution.form_name')}</label>
            <input
              className={styles.formInput}
              value={form.name}
              placeholder={t('distribution.form_name_placeholder')}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              disabled={saving}
            />
          </div>
          <div className={styles.formFieldRow}>
            <ToggleSwitch
              checked={form.enabled}
              onChange={(value) => setForm({ ...form, enabled: value })}
              label={t('distribution.form_enabled')}
            />
          </div>
          <div className={styles.formField}>
            <label className={styles.formLabel}>{t('distribution.form_expiry')}</label>
            <input
              className={styles.formInput}
              type="datetime-local"
              value={form.expiresAtLocal}
              onChange={(e) => setForm({ ...form, expiresAtLocal: e.target.value })}
              disabled={saving}
              required
            />
            <div className={styles.formHint}>{t('distribution.form_expiry_hint')}</div>
          </div>
          <div className={styles.formField}>
            <label className={styles.formLabel}>{t('distribution.form_models')}</label>
            <textarea
              className={styles.formTextarea}
              value={form.allowedModelsText}
              rows={4}
              placeholder={t('distribution.form_models_placeholder')}
              onChange={(e) => setForm({ ...form, allowedModelsText: e.target.value })}
              disabled={saving}
            />
            <div className={styles.formHint}>{t('distribution.form_models_hint')}</div>
          </div>
          <div className={styles.formField}>
            <label className={styles.formLabel}>{t('distribution.form_quota')}</label>
            <input
              className={styles.formInput}
              value={form.quotaUsdText}
              inputMode="decimal"
              placeholder={t('distribution.form_quota_placeholder')}
              onChange={(e) => setForm({ ...form, quotaUsdText: e.target.value })}
              disabled={saving}
            />
            <div className={styles.formHint}>{t('distribution.form_quota_hint')}</div>
          </div>
          {formError && <div className={styles.errorText}>{formError}</div>}
        </div>
      </Modal>

      <Modal
        open={createdKey !== null}
        onClose={() => setCreatedKey(null)}
        title={t('distribution.created_title')}
        footer={
          <Button variant="primary" onClick={() => setCreatedKey(null)}>
            {t('common.confirm')}
          </Button>
        }
      >
        <div className={styles.form}>
          <div className={styles.revealBox}>
            <code>{createdKey?.key}</code>
          </div>
          <div className={styles.formHint}>{t('distribution.created_hint')}</div>
          <Button variant="secondary" onClick={() => void copyText(createdKey?.key ?? '')}>
            {t('distribution.copy_key')}
          </Button>
        </div>
      </Modal>
    </div>
  );
}
