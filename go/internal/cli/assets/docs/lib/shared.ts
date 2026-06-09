export const appName = 'WendyOS Docs';
export const basePath = process.env.NEXT_PUBLIC_BASE_PATH || '';

// Shared OpenGraph/Twitter card image. Served from the marketing site
// (wendy.dev) as an absolute URL so it works regardless of the docs deploy
// basePath (/latest, /release-x, /branch-main-<sha>).
export const ogImage = 'https://wendy.dev/images/og-image.png';
export const docsRoute = '/';

export const githubRepo = {
  user: 'wendylabsinc',
  repo: 'WendyOS',
};
