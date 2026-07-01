export type DocsAnalyticsEventName = 'cli_install_copy';

export type DocsAnalyticsEventParams = Record<string, string | number | boolean | null | undefined>;

declare global {
  interface Window {
    gtag?: (...args: unknown[]) => void;
  }
}

export function trackDocsAnalyticsEvent(
  eventName: DocsAnalyticsEventName,
  params: DocsAnalyticsEventParams = {},
) {
  if (typeof window === 'undefined' || typeof window.gtag !== 'function') return;

  window.gtag('event', eventName, {
    ...params,
    event_category: 'marketing',
  });
}
