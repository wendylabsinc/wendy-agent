import { Activity, Bot, BugPlay, CircuitBoard, Code2, LockOpen, Plug, RefreshCw } from 'lucide-react';
import type { ReactNode } from 'react';

type USP = {
  icon: ReactNode;
  title: string;
  description: string;
};

const usps: USP[] = [
  {
    icon: <Plug className="size-5 text-orange-500" />,
    title: 'USB-C Deployment',
    description:
      'Plug in your Jetson or Pi and deploy in seconds. No SSH, no network config, no ceremony.',
  },
  {
    icon: <BugPlay className="size-5 text-orange-500" />,
    title: 'Remote Debugging',
    description:
      'Full VSCode debugger with breakpoints over USB or the internet — on any device, anywhere.',
  },
  {
    icon: <Activity className="size-5 text-orange-500" />,
    title: 'Observability',
    description:
      'Real-time logs, metrics, and distributed traces from every device in your fleet.',
  },
  {
    icon: <Bot className="size-5 text-orange-500" />,
    title: 'ROS2 Support',
    description:
      'Run ROS2 nodes natively alongside WendyOS apps on the same hardware.',
  },
  {
    icon: <RefreshCw className="size-5 text-orange-500" />,
    title: 'Atomic OTA Updates',
    description:
      'Push firmware to one device or ten thousand. Automatic rollback on failure.',
  },
  {
    icon: <Code2 className="size-5 text-orange-500" />,
    title: 'VSCode & Cursor',
    description:
      'Deploy and debug without leaving your editor. Extensions for VSCode, Cursor, and Windsurf.',
  },
  {
    icon: <LockOpen className="size-5 text-orange-500" />,
    title: 'Apache 2.0',
    description:
      'No black boxes, no lock-in. The full OS and toolchain are open source and auditable.',
  },
  {
    icon: <CircuitBoard className="size-5 text-orange-500" />,
    title: 'NVIDIA & Raspberry Pi',
    description:
      'First-class support for Jetson Orin, AGX Thor, and Raspberry Pi 3, 4, and 5.',
  },
];

export function USPGrid() {
  return (
    <div className="not-prose my-8 grid gap-px bg-fd-border sm:grid-cols-2 lg:grid-cols-4">
      {usps.map((usp) => (
        <div
          key={usp.title}
          className="flex flex-col gap-3 bg-fd-card p-5 transition-colors hover:bg-fd-accent"
        >
          {usp.icon}
          <h3 className="text-sm font-semibold text-fd-card-foreground">{usp.title}</h3>
          <p className="text-sm leading-relaxed text-fd-muted-foreground">{usp.description}</p>
        </div>
      ))}
    </div>
  );
}
