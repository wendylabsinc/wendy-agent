import { GoogleAnalytics } from '@next/third-parties/google';
import Script from 'next/script';
import { withBasePath } from '@/lib/shared';

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
      <Script
        src={withBasePath('/static/docs-google-analytics-consent-default.js')}
        strategy="beforeInteractive"
      />
      <GoogleAnalytics gaId={GA_MEASUREMENT_ID} />
    </>
  );
}
