export const appName = 'WendyOS Docs';
export const basePath = process.env.NEXT_PUBLIC_BASE_PATH || '';
export const docsRoute = '/';
export const docsImageRoute = `${basePath}/og`;
export const docsContentRoute = `${basePath}/md`;

export const gitConfig = {
  user: 'wendylabsinc',
  repo: 'WendyOS',
  branch: process.env.NEXT_PUBLIC_GITHUB_REF_NAME || 'main',
};
