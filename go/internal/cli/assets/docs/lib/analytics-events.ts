export const DOCS_ANALYTICS_CONSENT_KEY = 'wendy_docs_analytics_consent';

export type DocsAnalyticsConsentState = 'granted' | 'denied';
export type DocsInstallCopyTarget = 'unix' | 'windows' | 'agent-linux';
export type DocsInstallCopyVariant =
  | 'cli for macOS/linux'
  | 'cli for windows'
  | 'wendy-agent for Linux';
export type DocsInstallCopyLabel = 'macOS/Linux CLI' | 'windows CLI' | 'wendy-agent for Linux';
export type DocsAnalyticsLocation =
  | 'docs_get_started_cli_install_command'
  | 'docs_get_started_agent_install_command'
  | 'docs_install_scripts_dialog';

export type DocsAnalyticsEvents = {
  cli_install_copy: {
    install_target: DocsInstallCopyTarget;
    install_variant: DocsInstallCopyVariant;
    install_label: DocsInstallCopyLabel;
    location: DocsAnalyticsLocation;
  };
};

export type DocsAnalyticsEventName = keyof DocsAnalyticsEvents;
export type DocsAnalyticsEventParams<T extends DocsAnalyticsEventName = DocsAnalyticsEventName> =
  DocsAnalyticsEvents[T];

export type DocsAnalyticsTrackingProps<T extends DocsAnalyticsEventName = DocsAnalyticsEventName> =
  | {
      analyticsEventName: T;
      analyticsEventParams: DocsAnalyticsEventParams<T>;
    }
  | {
      analyticsEventName?: undefined;
      analyticsEventParams?: undefined;
    };

declare global {
  interface Window {
    gtag?: (...args: unknown[]) => void;
  }
}

function isDocsAnalyticsConsentState(value: string | null): value is DocsAnalyticsConsentState {
  return value === 'granted' || value === 'denied';
}

function getStoredDocsAnalyticsConsent(): DocsAnalyticsConsentState | null {
  if (typeof window === 'undefined') return null;

  try {
    const stored = window.localStorage.getItem(DOCS_ANALYTICS_CONSENT_KEY);
    if (isDocsAnalyticsConsentState(stored)) return stored;
  } catch {
    // Storage can be unavailable in private browsing or restricted embeds.
  }

  if (typeof document === 'undefined') return null;

  const cookieValue =
    document.cookie
      .split('; ')
      .find((cookie) => cookie.startsWith(`${DOCS_ANALYTICS_CONSENT_KEY}=`))
      ?.split('=')
      .at(1) ?? null;

  return isDocsAnalyticsConsentState(cookieValue) ? cookieValue : null;
}

function updateGoogleAnalyticsConsent(state: DocsAnalyticsConsentState) {
  if (typeof window === 'undefined' || typeof window.gtag !== 'function') return;

  window.gtag('consent', 'update', {
    analytics_storage: state,
    ad_storage: 'denied',
    ad_user_data: 'denied',
    ad_personalization: 'denied',
  });
}

export function hasDocsAnalyticsConsent() {
  return getStoredDocsAnalyticsConsent() === 'granted';
}

export function setDocsAnalyticsConsent(state: DocsAnalyticsConsentState) {
  if (typeof window === 'undefined') return;

  try {
    window.localStorage.setItem(DOCS_ANALYTICS_CONSENT_KEY, state);
  } catch {
    // Storage can be unavailable in private browsing or restricted embeds.
  }

  if (typeof document !== 'undefined') {
    document.cookie = `${DOCS_ANALYTICS_CONSENT_KEY}=${state}; Path=/; Max-Age=31536000; SameSite=Lax; Secure`;
  }

  updateGoogleAnalyticsConsent(state);
}

export function trackDocsAnalyticsEvent<T extends DocsAnalyticsEventName>(
  eventName: T,
  params: DocsAnalyticsEventParams<T>,
) {
  if (typeof window === 'undefined' || typeof window.gtag !== 'function') return;
  if (getStoredDocsAnalyticsConsent() !== 'granted') return;

  updateGoogleAnalyticsConsent('granted');

  window.gtag('event', eventName, {
    ...params,
    // Docs install-copy events intentionally share the marketing-site GA4 schema.
    event_category: 'marketing',
  });
}
