import { Analytics } from '@/components/analytics';
import { Provider } from '@/components/provider';
import type { ReactNode } from 'react';
import { ogImage } from '@/lib/shared';
import './global.css';

const siteUrl = process.env.NEXT_PUBLIC_SITE_URL;

if (process.env.CI && !siteUrl) {
  throw new Error('NEXT_PUBLIC_SITE_URL must be set in CI docs builds');
}

export const metadata = {
  metadataBase: new URL(siteUrl ?? 'http://localhost:3000'),
  title: {
    default: 'WendyOS Docs',
    template: '%s | WendyOS Docs',
  },
  description: 'Developer documentation for WendyOS, wendy-agent, and the Wendy CLI.',
  openGraph: {
    type: 'website',
    siteName: 'WendyOS Docs',
    images: [{ url: ogImage, width: 1200, height: 630, alt: 'Wendy — Physical AI Platform' }],
  },
  twitter: {
    card: 'summary_large_image',
    images: [ogImage],
  },
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className="flex min-h-screen flex-col">
        <Provider>{children}</Provider>
        <Analytics />
      </body>
    </html>
  );
}
