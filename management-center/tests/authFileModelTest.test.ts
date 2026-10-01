import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { modelProbeApi } from '@/features/authFiles/modelProbe/api';
import {
  canTestModelProvider,
  modelTestResultView,
} from '@/features/authFiles/modelProbe/logic';
import { apiClient } from '@/services/api/client';

describe('canTestModelProvider', () => {
  test('gates the row action on backend probe drivers', () => {
    for (const provider of ['commandcode', 'opencode-go', 'claude', 'codex', 'cursor']) {
      expect(canTestModelProvider(provider)).toBe(true);
    }
    expect(canTestModelProvider('CommandCode')).toBe(true);
    expect(canTestModelProvider(' claude ')).toBe(true);
    expect(canTestModelProvider('gemini')).toBe(false);
    expect(canTestModelProvider('antigravity')).toBe(false);
    expect(canTestModelProvider(undefined)).toBe(false);
    expect(canTestModelProvider('')).toBe(false);
  });
});

describe('modelTestResultView', () => {
  test('null when never run, muted label while running', () => {
    expect(modelTestResultView(undefined, 'Testing…')).toBeNull();
    expect(modelTestResultView({ state: 'running' }, 'Testing…')).toEqual({
      tone: 'running',
      text: 'Testing…',
    });
  });

  test('ok rows show the trimmed reply with latency', () => {
    expect(
      modelTestResultView({ state: 'done', ok: true, reply_text: '  ok ok  ', latency_ms: 812 }, 'x')
    ).toEqual({ tone: 'ok', text: 'ok ok · 812 ms' });
    // Empty reply and missing latency stay graceful.
    expect(modelTestResultView({ state: 'done', ok: true }, 'x')).toEqual({
      tone: 'ok',
      text: '—',
    });
  });

  test('failed rows carry the status vocabulary prefix', () => {
    expect(
      modelTestResultView({ state: 'done', ok: false, status: 'auth_error', error: 'invalid api key' }, 'x')
    ).toEqual({ tone: 'error', text: 'auth_error: invalid api key' });
    expect(modelTestResultView({ state: 'done', ok: false, error: 'boom' }, 'x')).toEqual({
      tone: 'error',
      text: 'boom',
    });
  });
});

describe('modelProbeApi.testModel', () => {
  test('posts auth_file and model to /auth-files/test-model', async () => {
    const payload = { ok: true, model: 'm-1', status: 'usable', latency_ms: 100, reply_text: 'ok' };
    const post = spyOn(apiClient, 'post').mockResolvedValue(payload);
    try {
      const resp = await modelProbeApi.testModel('cc.json', 'm-1');
      expect(resp.ok).toBe(true);
      expect(post).toHaveBeenLastCalledWith(
        '/auth-files/test-model',
        { auth_file: 'cc.json', model: 'm-1' },
        undefined
      );
    } finally {
      post.mockRestore();
    }
  });
});

describe('model test locale keys', () => {
  test('run/running labels exist in all 4 locales', () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      expect(translations.auth_files.model_test_run).toBeTruthy();
      expect(translations.auth_files.model_test_running).toBeTruthy();
    }
  });
});
