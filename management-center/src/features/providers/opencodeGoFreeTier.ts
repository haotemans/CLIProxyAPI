export const OPENCODE_GO_PUBLIC_KEY = 'public';

/**
 * Effective key for zen-free detection: the freshly typed value wins, then the
 * stored one (edit mode keeps the field blank to leave it unchanged).
 */
export const opencodeGoEffectiveApiKey = (typed: string, stored: string | undefined): string => {
  const value = typed.trim();
  return value !== '' ? value : (stored ?? '').trim();
};

/** The literal "public" key selects the anonymous OpenCode Zen free tier. */
export const isOpencodeGoAnonymousFreeKey = (value: string): boolean =>
  value === OPENCODE_GO_PUBLIC_KEY;
