import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import zhCN from '../src/i18n/locales/zh-CN.json';
import { AuthFileProbeSection } from '../src/features/authFiles/modelProbe/AuthFileProbeSection';

describe('Auth file probe action', () => {
  test('keeps the action without the redundant result and timestamp row', async () => {
    const i18n = createInstance();
    await i18n.init({ lng: 'zh-CN', resources: { 'zh-CN': { translation: zhCN } } });
    const markup = renderToStaticMarkup(
      createElement(
        I18nextProvider,
        { i18n },
        createElement(AuthFileProbeSection, {
          file: { name: 'mirasim-test.json', type: 'mirasim', authIndex: 'test' },
          row: {
            probed: true,
            usable: 5,
            pruned: 2,
            checked_at: '2026-10-26T13:23:34Z',
            catalog_size: 15,
          },
          onProbed: () => {},
        })
      )
    );
    expect(markup).toContain(zhCN.auth_files.probe_run);
    expect(markup).not.toContain('模型探测');
    expect(markup).not.toContain('2026-10-26');
    expect(markup).not.toContain('剔除');
    expect(markup).not.toContain('✓');
  });
});
