import type { PluginListEntry } from '@/types';
import { getPluginTitle, resolvePluginAssetURL } from '@/features/plugins/pluginResources';

export interface PluginOAuthProviderCard {
  kind: 'plugin';
  id: string;
  title: string;
  icon: string;
}

/**
 * Build OAuth cards for plugin-provided providers. An entry participates when
 * it is registered, enabled, declares OAuth support and names an
 * `oauth_provider` not already covered by the static builtin cards (and not
 * provided by another plugin seen first).
 */
export const buildPluginOAuthProviderCards = (
  plugins: PluginListEntry[],
  apiBase: string,
  builtinIds: Set<string>
): PluginOAuthProviderCard[] => {
  const seenProviders = new Set(builtinIds);
  return plugins.flatMap((plugin) => {
    const provider = plugin.oauthProvider;
    if (
      !plugin.registered ||
      !plugin.supportsOAuth ||
      !plugin.effectiveEnabled ||
      !provider ||
      seenProviders.has(provider)
    ) {
      return [];
    }
    seenProviders.add(provider);
    return [
      {
        kind: 'plugin' as const,
        id: provider,
        title: getPluginTitle(plugin),
        icon: resolvePluginAssetURL(plugin.logo || plugin.metadata?.logo || '', apiBase),
      },
    ];
  });
};

/**
 * Plugin OAuth start URLs may be root-relative paths (e.g.
 * `/v0/resource/plugins/mirasim/oauth/start?state=...`). Resolve those
 * against the given origin so Copy Link and window.open always have an
 * absolute URL; absolute http(s) URLs pass through unchanged.
 */
export const resolveAuthStartUrl = (origin: string, value: string): string => {
  const trimmed = (value ?? '').trim();
  if (!trimmed) return trimmed;
  if (trimmed.startsWith('/')) {
    return new URL(trimmed, origin).toString();
  }
  return trimmed;
};
