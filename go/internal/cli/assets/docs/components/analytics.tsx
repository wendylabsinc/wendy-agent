import { GoogleAnalytics } from '@next/third-parties/google';
import Script from 'next/script';

const GOOGLE_CONSENT_DEFAULT = `
(function() {
  var analyticsConsent = 'denied';
  try {
    var stored = window.localStorage.getItem('wendy_docs_analytics_consent');
    var cookieMatch = document.cookie.match(/(?:^|; )wendy_docs_analytics_consent=(granted|denied)(?:;|$)/);
    if (stored === 'granted' || stored === 'denied') {
      analyticsConsent = stored;
    } else if (cookieMatch) {
      analyticsConsent = cookieMatch[1];
    }
  } catch (error) {}

  window.dataLayer = window.dataLayer || [];
  window.gtag = window.gtag || function(){ window.dataLayer.push(arguments); };
  window.gtag('consent', 'default', {
    analytics_storage: analyticsConsent,
    ad_storage: 'denied',
    ad_user_data: 'denied',
    ad_personalization: 'denied'
  });
})();
`;

// Google Analytics 4 / Firebase Analytics — shares the same measurement ID as
// the marketing site (wendy.dev): the "marketing-website-wendy" Firebase web
// app (project cloud-c7e56), so docs traffic lands in the same GA4 property.
//
// @next/third-parties injects gtag.js via next/script and tracks App Router
// route changes automatically. Gated on the env var so local dev (where it is
// unset) sends nothing; CI sets it at build time for deployed docs.
const GA_MEASUREMENT_ID = process.env.NEXT_PUBLIC_GA_MEASUREMENT_ID;

export function Analytics() {
  if (!GA_MEASUREMENT_ID) {
    return null;
  }

  return (
    <>
      <Script id="docs-google-analytics-consent-default" strategy="beforeInteractive">
        {GOOGLE_CONSENT_DEFAULT}
      </Script>
      <GoogleAnalytics gaId={GA_MEASUREMENT_ID} />
    </>
  );
}
