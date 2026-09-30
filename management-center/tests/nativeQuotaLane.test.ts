import { describe, expect, test } from 'bun:test';
import { readNativeQuotaData, type QuotaFetchPayload } from '@/features/quota/providers/native/requests';
import { KIRO_CONFIG } from '@/features/quota/providers/kiro/data';
import { MIRASIM_CONFIG } from '@/features/quota/providers/mirasim/data';
import type { QuotaProviderType } from '@/features/quota/providers/types';

describe('readNativeQuotaData', () => {
  test('maps groups/buckets/subscription onto rows', () => {
    const payload: QuotaFetchPayload = {
      subscription: { plan: 'KIRO FREE' },
      groups: [
        {
          displayName: 'credits',
          buckets: [
            {
              remainingFraction: 0.6,
              resetTime: '2026-10-05T00:00:00Z',
              description: '40.0 / 100 credits',
            },
          ],
        },
      ],
    };
    const data = readNativeQuotaData(payload);
    expect(data.plan).toBe('KIRO FREE');
    expect(data.rows).toHaveLength(1);
    const row = data.rows[0];
    expect(row.limit).toBe(100);
    expect(row.used).toBeCloseTo(40);
    expect(row.resetAtMs).toBe(Date.parse('2026-10-05T00:00:00Z'));
    expect(row.label).toBe('credits · 40.0 / 100 credits');
  });

  test('tolerates snake_case variants and limits fraction range', () => {
    const payload: QuotaFetchPayload = {
      groups: [
        {
          display_name: 'weekly',
          buckets: [{ remaining_fraction: 1.7, reset_time: 'soon' }],
        },
      ],
    };
    const data = readNativeQuotaData(payload);
    expect(data.rows).toHaveLength(1);
    expect(data.rows[0].used).toBeCloseTo(0);
    expect(data.rows[0].resetAtMs).toBeNull();
  });

  test('skips buckets without a numeric fraction', () => {
    const data = readNativeQuotaData({
      groups: [{ displayName: 'week', buckets: [{ description: 'no fraction' }] }],
    });
    expect(data.rows).toHaveLength(0);
  });

  test('tier name fallback when plan is missing', () => {
    const data = readNativeQuotaData({ subscription: { tierName: 'PRO' } });
    expect(data.plan).toBe('PRO');
  });
});

describe('native lane configs', () => {
  test('kiro lane registers as kiro with the kiro store map', () => {
    expect(KIRO_CONFIG.type as QuotaProviderType).toBe('kiro');
    expect(KIRO_CONFIG.i18nPrefix).toBe('kiro_quota');
    expect(KIRO_CONFIG.storeSetter).toBe('setKiroQuota');
  });

  test('mirasim lane registers as mirasim with the mirasim store map', () => {
    expect(MIRASIM_CONFIG.type as QuotaProviderType).toBe('mirasim');
    expect(MIRASIM_CONFIG.i18nPrefix).toBe('mirasim_quota');
    expect(MIRASIM_CONFIG.storeSetter).toBe('setMirasimQuota');
  });

  test('buildSuccessState flattens plan/rows into state', () => {
    const state = KIRO_CONFIG.buildSuccessState({
      plan: 'FREE',
      rows: [{ id: '1', used: 25, limit: 100 }],
    } as never);
    expect(state.status).toBe('success');
    expect(state.plan).toBe('FREE');
    expect(state.rows).toHaveLength(1);
  });

  test('filters only enabled files from its provider', () => {
    const file = { provider: 'kiro', name: 'kiro-a.json' } as never;
    const wrong = { provider: 'cursor', name: 'c.json' } as never;
    expect(KIRO_CONFIG.filterFn(file)).toBe(true);
    expect(KIRO_CONFIG.filterFn(wrong)).toBe(false);
  });
});
