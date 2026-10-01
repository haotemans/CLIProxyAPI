import { describe, expect, test } from 'bun:test';
import {
  filterPricingEntries,
  formatPrice,
  isValidPriceInput,
} from '../src/features/usageStats/eventsLogic';

describe('pricing search filter', () => {
  const entries = [
    { model: 'claude-opus-4-5', input: 5, output: 25, source: 'default' },
    { model: 'claude-haiku-3', input: 1, output: 5, source: 'litellm' },
    { model: 'gpt-5', input: 1.25, output: 10, source: 'user' },
    { model: 'deepseek/deepseek-v4.1-flash', input: 0.27, output: 1.1, source: 'litellm' },
  ];

  test('empty search returns the input unchanged (same order)', () => {
    expect(filterPricingEntries(entries, '')).toBe(entries);
    expect(filterPricingEntries(entries, '   ')).toBe(entries);
  });

  test('search matches case-insensitively across model ids', () => {
    expect(filterPricingEntries(entries, 'CLAUDE').map((e) => e.model)).toEqual([
      'claude-opus-4-5',
      'claude-haiku-3',
    ]);
    expect(filterPricingEntries(entries, 'gpt')).toEqual([entries[2]]);
    expect(filterPricingEntries(entries, 'deepseek-v4')).toEqual([entries[3]]);
    expect(filterPricingEntries(entries, 'zzz-nothing')).toEqual([]);
  });
});

describe('pricing inline edit validation', () => {
  test('accepts finite non-negative numbers including zero', () => {
    expect(isValidPriceInput('0', '0')).toBe(true);
    expect(isValidPriceInput('1.25', ' 10 ')).toBe(true);
    expect(isValidPriceInput('0.0001', '2')).toBe(true);
  });

  test('rejects blanks, negatives and garbage', () => {
    expect(isValidPriceInput('', '1')).toBe(false);
    expect(isValidPriceInput('1', 'abc')).toBe(false);
    expect(isValidPriceInput('-0.5', '1')).toBe(false);
    expect(isValidPriceInput('1', 'Infinity,oops')).toBe(false);
  });
});

describe('price formatting', () => {
  test('renders zero, fractions and large values readably', () => {
    expect(formatPrice(0)).toBe('$0');
    expect(formatPrice(Number.NaN)).toBe('-');
    expect(formatPrice(75)).toBe('$75');
    expect(formatPrice(1.25)).toBe('$1.25');
    expect(formatPrice(0.0004)).toMatch(/^\$0\.0004/);
  });
});
