import { Inter } from 'next/font/google';
import { Provider } from '@/components/provider';
import type { ReactNode } from 'react';
import './global.css';

const inter = Inter({
  subsets: ['latin'],
});

export const metadata = {
  metadataBase: new URL(process.env.NEXT_PUBLIC_SITE_URL || 'https://docs.wendy.dev'),
  title: {
    default: 'WendyOS Docs',
    template: '%s | WendyOS Docs',
  },
  description: 'Developer documentation for WendyOS, wendy-agent, and the Wendy CLI.',
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" className={inter.className} suppressHydrationWarning>
      <body className="flex min-h-screen flex-col">
        <Provider>{children}</Provider>
      </body>
    </html>
  );
}
