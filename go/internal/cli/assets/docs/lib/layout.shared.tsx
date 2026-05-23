import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';
import { appName, githubRepo } from './shared';

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: appName,
    },
    githubUrl: `https://github.com/${githubRepo.user}/${githubRepo.repo}`,
  };
}
