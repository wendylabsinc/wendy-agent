'use client';

import { ArrowRight } from 'lucide-react';
import { useEffect, useState } from 'react';
import {
  agentCurlCommand,
  cliCurlCommand,
  cliWingetCommand,
  InstallCommand,
} from '@/components/docs/install-command';

type CliPlatform = 'unix' | 'windows';

const cliPlatforms: Array<{
  id: CliPlatform;
  label: string;
  command: string;
}> = [
  {
    id: 'unix',
    label: 'macOS/Linux',
    command: cliCurlCommand,
  },
  {
    id: 'windows',
    label: 'Windows',
    command: cliWingetCommand,
  },
];

function detectCliPlatform(): CliPlatform {
  if (typeof navigator === 'undefined') return 'unix';

  const agent = `${navigator.userAgent} ${navigator.platform}`;
  return /\bWin/i.test(agent) ? 'windows' : 'unix';
}

export function GetStartedSection() {
  const [selectedPlatform, setSelectedPlatform] = useState<CliPlatform>('unix');
  const activePlatform =
    cliPlatforms.find((platform) => platform.id === selectedPlatform) ?? cliPlatforms[0];

  useEffect(() => {
    setSelectedPlatform(detectCliPlatform());
  }, []);

  return (
    <section className="not-prose my-10 border-y py-8">
      <div className="grid gap-8 lg:grid-cols-[minmax(260px,0.8fr)_minmax(440px,1.2fr)] lg:items-start">
        <div>
          <p className="font-mono text-[11px] font-medium tracking-widest text-wendy-seafoam uppercase">
            Get Started
          </p>
          <h2 className="mt-2 text-2xl font-semibold text-fd-foreground">Install Wendy</h2>
          <p className="mt-3 max-w-2xl text-sm leading-relaxed text-fd-muted-foreground">
            Plug in an NVIDIA Jetson or Raspberry Pi over USB and start deploying in seconds.{' '}
            <a
              href="/installation/wendy-agent-macos"
              className="font-medium text-wendy-seafoam no-underline transition-colors hover:text-wendy-seafoam-hover"
            >
              Wendy for macOS
            </a>{' '}
            is available in beta for Apple Silicon targets.
          </p>
        </div>

        <div className="space-y-7">
          <section>
            <div className="flex flex-wrap items-end justify-between gap-3">
              <div>
                <h3 className="text-base font-semibold text-fd-foreground">Wendy CLI</h3>
                <p className="mt-1 text-sm text-fd-muted-foreground">
                  Install this on your developer machine.
                </p>
              </div>

              <div
                role="tablist"
                aria-label="Wendy CLI install platform"
                className="grid grid-cols-2 border bg-fd-secondary/40 p-1"
              >
                {cliPlatforms.map((platform) => {
                  const active = platform.id === selectedPlatform;

                  return (
                    <button
                      key={platform.id}
                      type="button"
                      role="tab"
                      aria-selected={active}
                      onClick={() => setSelectedPlatform(platform.id)}
                      className={
                        active
                          ? 'bg-fd-primary px-3 py-1.5 text-xs font-medium text-fd-primary-foreground'
                          : 'px-3 py-1.5 text-xs font-medium text-fd-muted-foreground transition-colors hover:text-fd-foreground'
                      }
                    >
                      {platform.label}
                    </button>
                  );
                })}
              </div>
            </div>

            <InstallCommand command={activePlatform.command} />
          </section>

          <section className="border-t pt-6">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-base font-semibold text-fd-foreground">
                <code className="font-mono text-[0.95em]">wendy-agent</code> for Linux
              </h3>
              <span className="border border-wendy-seafoam/40 px-2 py-0.5 text-[11px] font-medium text-wendy-seafoam">
                Optional
              </span>
            </div>
            <p className="mt-1 text-sm text-fd-muted-foreground">
              Install this on an existing Linux target. WendyOS images already include it.
            </p>
            <InstallCommand command={agentCurlCommand} />
          </section>

          <section className="border-t pt-6">
            <h3 className="text-base font-semibold text-fd-foreground">
              Developer machine setup
            </h3>
            <p className="mt-1 text-sm text-fd-muted-foreground">
              Configure your local tools, editor, and first device connection.
            </p>
            <a
              href="/installation/developer-machine-setup"
              className="mt-3 inline-flex items-center gap-2 bg-fd-primary px-4 py-2 text-sm font-medium text-fd-primary-foreground no-underline transition-transform hover:translate-x-0.5"
            >
              Open setup guide
              <ArrowRight className="size-4" />
            </a>
          </section>
        </div>
      </div>
    </section>
  );
}
