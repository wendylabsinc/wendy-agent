'use client';

import { CodeBlock } from 'fumadocs-ui/components/codeblock';
import { type ComponentProps, createContext, type ReactNode, use } from 'react';
import { type DocsAnalyticsEventParams, trackDocsAnalyticsEvent } from '@/lib/analytics-events';
import { cn } from '@/lib/cn';

type CliInstallCopyParams = DocsAnalyticsEventParams<'cli_install_copy'>;

const quickstartCliInstallCopies = {
  unix: {
    install_target: 'unix',
    install_variant: 'cli for macOS/linux',
    install_label: 'macOS/Linux CLI',
    location: 'docs_quickstart_cli_install_command',
  },
  windows: {
    install_target: 'windows',
    install_variant: 'cli for windows',
    install_label: 'windows CLI',
    location: 'docs_quickstart_cli_install_command',
  },
} satisfies Record<string, CliInstallCopyParams>;

const InstallCopyContext = createContext<CliInstallCopyParams | null>(null);

// Wraps a Quickstart install code block in MDX so its copy button sends
// cli_install_copy. It renders no element, so a code block that is the only
// child of a Tab keeps the tab's full-width styling.
export function TrackQuickstartInstallCopy({
  target,
  children,
}: {
  target: keyof typeof quickstartCliInstallCopies;
  children: ReactNode;
}) {
  return (
    <InstallCopyContext value={quickstartCliInstallCopies[target] ?? null}>
      {children}
    </InstallCopyContext>
  );
}

// Renders every MDX code block. Outside TrackQuickstartInstallCopy it is the
// default Fumadocs code block.
export function TrackedCodeBlock(props: ComponentProps<typeof CodeBlock>) {
  const params = use(InstallCopyContext);
  if (!params) return <CodeBlock {...props} />;

  return (
    <CodeBlock
      {...props}
      // Same wrapper as the Fumadocs default; the copy button is its only child.
      Actions={({ className, children }) => (
        <div
          className={cn('empty:hidden', className)}
          onClick={() => trackDocsAnalyticsEvent('cli_install_copy', params)}
        >
          {children}
        </div>
      )}
    />
  );
}
