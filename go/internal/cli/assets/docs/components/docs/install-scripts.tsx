'use client';

import { Check, Copy, Terminal, X } from 'lucide-react';
import { useRef, useState } from 'react';

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);

  return (
    <button
      type="button"
      aria-label="Copy to clipboard"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          /* clipboard unavailable */
        }
      }}
      className="shrink-0 border-l p-2.5 text-fd-muted-foreground transition-colors hover:bg-fd-accent hover:text-fd-accent-foreground"
    >
      {copied ? <Check className="size-4 text-green-500" /> : <Copy className="size-4" />}
    </button>
  );
}

function Command({ label, command }: { label?: string; command: string }) {
  return (
    <div className="mt-2">
      {label ? (
        <p className="mb-1 text-xs font-medium text-fd-muted-foreground">{label}</p>
      ) : null}
      <div className="flex items-stretch border bg-fd-secondary/40">
        <code className="flex-1 overflow-x-auto whitespace-nowrap px-3 py-2.5 font-mono text-sm text-fd-foreground">
          {command}
        </code>
        <CopyButton text={command} />
      </div>
    </div>
  );
}

export function InstallScripts() {
  const dialogRef = useRef<HTMLDialogElement>(null);

  // Native <dialog> rendered with showModal() lives in the browser top layer,
  // so it covers the whole viewport regardless of any transformed/overflow
  // ancestor (e.g. the sidebar this trigger sits in).
  return (
    <>
      <button
        type="button"
        onClick={() => dialogRef.current?.showModal()}
        className="inline-flex items-center gap-1.5 border px-2.5 py-1.5 text-sm font-medium text-fd-foreground transition-colors hover:bg-fd-accent hover:text-fd-accent-foreground"
      >
        <Terminal className="size-4" />
        Install Scripts
      </button>

      <dialog
        ref={dialogRef}
        aria-label="Install Wendy"
        onClick={(e) => {
          if (e.target === dialogRef.current) dialogRef.current?.close();
        }}
        className="m-auto w-full max-w-lg border bg-fd-popover p-0 text-fd-popover-foreground shadow-lg backdrop:bg-black/50 sm:max-w-xl"
      >
        <div className="relative">
          <button
            type="button"
            aria-label="Close"
            onClick={() => dialogRef.current?.close()}
            className="absolute right-3 top-3 p-1 text-fd-muted-foreground transition-colors hover:text-fd-foreground"
          >
            <X className="size-4" />
          </button>

          <div className="max-h-[85vh] overflow-y-auto p-6">
            <h2 className="text-lg font-semibold">Install Wendy</h2>

            <section className="mt-5">
              <h3 className="font-medium">Install Wendy CLI</h3>
              <p className="mt-1 text-sm text-fd-muted-foreground">
                Install this on your developer machine or continuous integration machine.
              </p>
              <Command
                label="macOS / Linux"
                command="curl -fsSL https://install.wendy.sh/cli.sh | bash"
              />
              <Command
                label="Windows"
                command="winget install WendyLabs.Wendy --source winget"
              />
            </section>

            <section className="mt-6">
              <h3 className="font-medium">
                Install <code className="font-mono text-[0.95em]">wendy-agent</code>
              </h3>
              <p className="mt-1 text-sm text-fd-muted-foreground">
                Install this on your Linux machine. You do <strong>not</strong> need to do this for
                WendyOS — it&apos;s already there!
              </p>
              <Command
                label="Linux"
                command="curl -fsSL https://install.wendy.sh/agent.sh | bash"
              />
            </section>
          </div>
        </div>
      </dialog>
    </>
  );
}
