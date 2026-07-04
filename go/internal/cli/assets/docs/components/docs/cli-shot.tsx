/**
 * Theme-aware renderers for the captures produced by the CLI screenshot
 * pipeline (see `screenshots/`). Each capture exists as a light and a dark
 * variant; these components render both and let CSS pick one — the light image
 * shows normally, the dark image when the site is in dark mode (fumadocs uses
 * class-based dark mode, so Tailwind's `dark:` variants apply).
 *
 * Paths follow the pipeline's naming contract:
 *   /images/docs/cli/<flow>/<step>-<theme>.png   (stills, via <CliShot>)
 *   /images/docs/cli/<flow>/<flow>-<theme>.webp   (clips,  via <CliClip>)
 */

// Mirror scripts/prepare-content.mjs: asset URLs are prefixed with the public
// base path (empty in dev, set for the exported site).
const basePath = (process.env.NEXT_PUBLIC_BASE_PATH || '').replace(/\/$/, '');

function assetUrl(path: string) {
  return `${basePath}/${path.replace(/^\/+/, '')}`;
}

// VHS renders at a fixed 1200×720 (see screenshots/lib/common.tape); declaring
// it avoids layout shift while CSS scales the image to the content width.
const WIDTH = 1200;
const HEIGHT = 720;

interface CliShotProps {
  /** Flow name, e.g. "wifi-connect" — the tapes/<flow>.tape basename. */
  flow: string;
  /** Step name, e.g. "select-device" — the Screenshot <step>.png basename. */
  step: string;
  /** Accessible description of what the screenshot shows. */
  alt: string;
}

export function CliShot({ flow, step, alt }: CliShotProps) {
  const src = (theme: 'light' | 'dark') =>
    assetUrl(`images/docs/cli/${flow}/${step}-${theme}.png`);

  return <ThemedImage light={src('light')} dark={src('dark')} alt={alt} />;
}

interface CliClipProps {
  /** Flow name, e.g. "wifi-connect" — the animated clip is named after it. */
  flow: string;
  /** Accessible description of what the clip shows. */
  alt: string;
}

export function CliClip({ flow, alt }: CliClipProps) {
  const src = (theme: 'light' | 'dark') =>
    assetUrl(`images/docs/cli/${flow}/${flow}-${theme}.webp`);

  return <ThemedImage light={src('light')} dark={src('dark')} alt={alt} />;
}

function ThemedImage({
  light,
  dark,
  alt,
}: {
  light: string;
  dark: string;
  alt: string;
}) {
  return (
    <span className="my-6 block">
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img
        src={light}
        alt={alt}
        width={WIDTH}
        height={HEIGHT}
        loading="lazy"
        decoding="async"
        className="block h-auto w-full dark:hidden"
      />
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img
        src={dark}
        alt={alt}
        width={WIDTH}
        height={HEIGHT}
        loading="lazy"
        decoding="async"
        className="hidden h-auto w-full dark:block"
      />
    </span>
  );
}
