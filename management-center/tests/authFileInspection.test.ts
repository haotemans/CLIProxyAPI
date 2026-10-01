import { describe, expect, test } from 'bun:test';
import {
  healthChipVariant,
  healthRank,
  indexInspectionByFile,
  KNOWN_SUGGESTIONS,
  type InspectionCredential,
} from '../src/features/authFiles/inspection';

const makeCred = (file: string, health: string): InspectionCredential => ({
  auth_file: file,
  provider: 'codex',
  health,
  signals: {
    disabled: false,
    status: 'active',
    quota_exceeded: false,
    unauthorized: false,
    requests_24h: 3,
    errors_24h: 0,
    probe_auth_error: false,
  },
  suggestions: health === 'good' ? [] : ['relogin'],
});

describe('pool inspection indexing', () => {
  test('indexes credentials by auth_file for card lookup', () => {
    const byFile = indexInspectionByFile([makeCred('a.json', 'good'), makeCred('b.json', 'bad')]);
    expect(Object.keys(byFile).sort()).toEqual(['a.json', 'b.json']);
    expect(byFile['b.json'].health).toBe('bad');
  });

  test('tolerates undefined and rogue entries', () => {
    expect(indexInspectionByFile(undefined)).toEqual({});
    expect(indexInspectionByFile([makeCred('', 'warn')])).toEqual({});
  });

  test('a missing card file stays ungraded (no chip)', () => {
    const byFile = indexInspectionByFile([makeCred('a.json', 'good')]);
    expect(byFile['other.json']).toBeUndefined();
  });
});

describe('pool inspection presentation helpers', () => {
  test('health ranks order bad before warn before good', () => {
    expect(healthRank('bad')).toBeLessThan(healthRank('warn'));
    expect(healthRank('warn')).toBeLessThan(healthRank('good'));
    expect(healthRank('unknown')).toBe(2);
  });

  test('chip variants fall back to good styling for unknown grades', () => {
    expect(healthChipVariant('bad')).toBe('bad');
    expect(healthChipVariant('warn')).toBe('warn');
    expect(healthChipVariant('anything')).toBe('good');
  });

  test('backend suggestion codes stay inside the localized set', () => {
    expect([...KNOWN_SUGGESTIONS].sort()).toEqual(['delete', 'relogin', 'rotate']);
  });
});
