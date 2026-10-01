import { describe, expect, test } from 'bun:test';
import { buildEventsParams } from '../src/features/usageStats/api';
import {
  clampEventPage,
  defaultEventFilter,
  formatEventTs,
  formatLatency,
  totalEventPages,
} from '../src/features/usageStats/eventsLogic';

describe('usage events params', () => {
  test('serializes only populated filter fields', () => {
    expect(
      buildEventsParams({
        from: 1000,
        to: 2000,
        provider: ' codex ',
        model: '',
        authFile: 'x.json',
        status: 'error',
        page: 2,
        pageSize: 50,
      })
    ).toEqual({
      from: 1000,
      to: 2000,
      provider: 'codex',
      auth_file: 'x.json',
      status: 'error',
      page: 2,
      page_size: 50,
    });
  });

  test('drops empty window bounds and empty text filters', () => {
    expect(
      buildEventsParams({
        from: 0,
        to: 0,
        provider: '   ',
        model: '',
        authFile: '',
        status: '',
        page: 1,
        pageSize: 50,
      })
    ).toEqual({ page: 1, page_size: 50 });
  });
});

describe('usage events pagination', () => {
  test('total pages rounds up and never drops below one', () => {
    expect(totalEventPages(0, 50)).toBe(1);
    expect(totalEventPages(1, 50)).toBe(1);
    expect(totalEventPages(50, 50)).toBe(1);
    expect(totalEventPages(51, 50)).toBe(2);
    expect(totalEventPages(201, 50)).toBe(5);
    expect(totalEventPages(10, 0)).toBe(1);
  });

  test('clamps pages into the valid range', () => {
    expect(clampEventPage(-3, 5)).toBe(1);
    expect(clampEventPage(1.9, 5)).toBe(1);
    expect(clampEventPage(3, 5)).toBe(3);
    expect(clampEventPage(99, 5)).toBe(5);
    expect(clampEventPage(2, 0)).toBe(1);
  });
});

describe('usage events defaults and formatting', () => {
  test('default filter uses the shared window and page 1', () => {
    expect(defaultEventFilter(10, 20)).toEqual({
      from: 10,
      to: 20,
      provider: '',
      model: '',
      authFile: '',
      status: '',
      page: 1,
      pageSize: 50,
    });
  });

  test('formats timestamps at second precision and tolerates junk', () => {
    expect(formatEventTs(0)).toBe('-');
    expect(formatEventTs(Number.NaN)).toBe('-');
    const rendered = formatEventTs(Date.UTC(2026, 9, 1, 12, 30, 45));
    expect(rendered).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
  });

  test('formats latency compactly', () => {
    expect(formatLatency(-1)).toBe('-');
    expect(formatLatency(0)).toBe('0ms');
    expect(formatLatency(999)).toBe('999ms');
    expect(formatLatency(1200)).toBe('1.2s');
  });
});
