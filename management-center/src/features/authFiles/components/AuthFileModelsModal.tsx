import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import type { AuthFileModelItem } from '@/features/authFiles/constants';
import { isModelExcluded } from '@/features/authFiles/constants';
import { modelProbeApi } from '@/features/authFiles/modelProbe/api';
import {
  canTestModelProvider,
  MODEL_TEST_STATUS_TEXT_KEYS,
  modelTestResultView,
  type ModelTestOutcome,
} from '@/features/authFiles/modelProbe/logic';
import styles from './AuthFileModelsModal.module.scss';

export type AuthFileModelsModalProps = {
  open: boolean;
  fileName: string;
  fileType: string;
  /** 当前凭证的 auth_index；有此值即启用「单模型测试」入口（POST /model-test）。 */
  authIndex?: string;
  loading: boolean;
  error: 'unsupported' | null;
  models: AuthFileModelItem[];
  excluded: Record<string, string[]>;
  onClose: () => void;
  onCopyText: (text: string) => void;
};

export function AuthFileModelsModal(props: AuthFileModelsModalProps) {
  const { t } = useTranslation();
  const {
    open,
    fileName,
    fileType,
    authIndex,
    loading,
    error,
    models,
    excluded,
    onClose,
    onCopyText,
  } = props;

  const [tests, setTests] = useState<Record<string, ModelTestOutcome>>({});
  useEffect(() => {
    setTests({});
  }, [fileName, open]);

  const canTest = canTestModelProvider(fileType) && Boolean(authIndex);
  const runTest = async (modelId: string) => {
    if (!authIndex || tests[modelId]?.state === 'running') return;
    setTests((prev) => ({ ...prev, [modelId]: { state: 'running' } }));
    try {
      const response = await modelProbeApi.testModelByIndex(authIndex, modelId);
      setTests((prev) => ({ ...prev, [modelId]: { state: 'done', ...response } }));
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setTests((prev) => ({ ...prev, [modelId]: { state: 'done', ok: false, error: message } }));
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('auth_files.models_title', { defaultValue: '支持的模型' }) + ` - ${fileName}`}
      footer={
        <Button variant="secondary" onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      {loading ? (
        <div className="hint">
          {t('auth_files.models_loading', { defaultValue: '正在加载模型列表...' })}
        </div>
      ) : error === 'unsupported' ? (
        <EmptyState
          title={t('auth_files.models_unsupported', { defaultValue: '当前版本不支持此功能' })}
          description={t('auth_files.models_unsupported_desc', {
            defaultValue: '请更新 CLI Proxy API 到最新版本后重试',
          })}
        />
      ) : models.length === 0 ? (
        <EmptyState
          title={t('auth_files.models_empty', { defaultValue: '该凭证暂无可用模型' })}
          description={t('auth_files.models_empty_desc', {
            defaultValue: '该认证凭证可能尚未被服务器加载或没有绑定任何模型',
          })}
        />
      ) : (
        <div className={styles.list}>
          {models.map((model) => {
            const excludedModel = isModelExcluded(model.id, fileType, excluded);
            const outcome = tests[model.id];
            const result = modelTestResultView(outcome, t('auth_files.model_test_running'));
            return (
              <div
                key={model.id}
                className={`${styles.item} ${excludedModel ? styles.itemExcluded : ''}`}
                onClick={() => {
                  onCopyText(model.id);
                }}
                title={
                  excludedModel
                    ? t('auth_files.models_excluded_hint', {
                        defaultValue: '此 OAuth 模型已被禁用',
                      })
                    : t('common.copy', { defaultValue: '点击复制' })
                }
              >
                <span className={styles.modelId}>{model.id}</span>
                {model.display_name && model.display_name !== model.id && (
                  <span className={styles.modelDisplayName}>{model.display_name}</span>
                )}
                {model.type && <span className={styles.modelType}>{model.type}</span>}
                {model.free === true && (
                  <span className={styles.freeBadge}>{t('auth_files.models_free_badge')}</span>
                )}
                {canTest && (
                  <button
                    type="button"
                    className={styles.testRun}
                    disabled={outcome?.state === 'running'}
                    onClick={(event) => {
                      event.stopPropagation();
                      void runTest(model.id);
                    }}
                  >
                    {t('auth_files.model_test_run')}
                  </button>
                )}
                {excludedModel && (
                  <span className={styles.excludedBadge}>
                    {t('auth_files.models_excluded_badge', { defaultValue: '已禁用' })}
                  </span>
                )}
                {result?.status && (
                  <span
                    className={`${styles.testStatus} ${
                      result.status === 'usable'
                        ? styles.testStatusUsable
                        : styles.testStatusError
                    }`}
                  >
                    {t(MODEL_TEST_STATUS_TEXT_KEYS[result.status] ?? result.status)}
                  </span>
                )}
                {result && (
                  <span
                    className={`${styles.testResult} ${
                      result.tone === 'ok'
                        ? styles.testResultOk
                        : result.tone === 'error'
                          ? styles.testResultError
                          : ''
                    }`}
                  >
                    {result.text}
                  </span>
                )}
              </div>
            );
          })}
        </div>
      )}
    </Modal>
  );
}
