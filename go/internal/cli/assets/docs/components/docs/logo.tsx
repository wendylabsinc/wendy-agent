/**
 * WendyOS wordmark for the docs nav. The emblem uses `currentColor` so it
 * follows the active Fumadocs theme (light/dark) rather than the OS scheme.
 */
export function Logo() {
  return (
    <span className="inline-flex items-center gap-2">
      <svg
        viewBox="0 0 246.82 181.81"
        className="h-5 w-auto"
        fill="currentColor"
        aria-hidden="true"
      >
        <rect
          x="91.64"
          y="26.62"
          width="128.56"
          height="128.56"
          transform="translate(-18.61 136.88) rotate(-45)"
        />
        <path d="M69.93,160.83L0,90.9,69.93,20.98l69.93,69.93-69.93,69.93ZM22.63,90.9l47.3,47.3,47.3-47.3-47.3-47.3-47.3,47.3Z" />
      </svg>
      <span className="font-semibold">WendyOS Docs</span>
    </span>
  );
}
