import { createMDX } from 'fumadocs-mdx/next';

const withMDX = createMDX();
const basePath = process.env.NEXT_PUBLIC_BASE_PATH || '';

if (basePath && !/^\/[a-z0-9][a-z0-9._-]{0,200}$/.test(basePath)) {
  throw new Error(`Invalid NEXT_PUBLIC_BASE_PATH: ${basePath}`);
}

/** @type {import('next').NextConfig} */
const config = {
  assetPrefix: basePath || undefined,
  basePath: basePath || undefined,
  images: {
    unoptimized: true,
  },
  output: 'export',
  reactStrictMode: true,
  trailingSlash: true,
};

export default withMDX(config);
