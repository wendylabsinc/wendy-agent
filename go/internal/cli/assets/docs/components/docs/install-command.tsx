'use client';

import { Check, Copy } from 'lucide-react';
import { useState } from 'react';

export const cliCurlCommand = 'curl -fsSL https://install.wendy.dev/cli.sh | bash';
export const cliWingetCommand = 'winget install WendyLabs.Wendy --source winget';
export const agentCurlCommand = 'curl -fsSL https://install.wendy.dev/agent.sh | bash';

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

export function InstallCommand({ label, command }: { label?: string; command: string }) {
  return (
    <div className="mt-2">
      {label ? (
        <p className="mb-1 text-xs font-medium text-fd-muted-foreground">{label}</p>
      ) : null}
      <div className="flex items-stretch border bg-fd-secondary/40">
        <code className="min-w-0 flex-1 whitespace-pre-wrap break-all px-3 py-2.5 font-mono text-sm text-fd-foreground">
          {command}
        </code>
        <CopyButton text={command} />
      </div>
    </div>
  );
}
