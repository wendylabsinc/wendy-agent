import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';
import { githubRepo } from './shared';
import { Logo } from '@/components/docs/logo';
import { InstallScripts } from '@/components/docs/install-scripts';

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: <Logo />,
    },
    githubUrl: `https://github.com/${githubRepo.user}/${githubRepo.repo}`,
    links: [
      {
        type: 'custom',
        secondary: true,
        children: <InstallScripts />,
      },
    ],
  };
}
