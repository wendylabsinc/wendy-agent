export function OverviewIntro() {
  return (
    <div className="not-prose mb-8 bg-zinc-950 p-8 sm:p-10">
      <p className="mb-4 font-mono text-[11px] font-medium tracking-widest text-wendy-seafoam uppercase">
        Physical AI Platform
      </p>
      <p className="text-xl font-semibold leading-snug text-white sm:text-2xl">
        The Full Stack Physical AI Platform
      </p>
      <p className="mt-3 max-w-2xl text-sm leading-relaxed text-zinc-400">
        Develop, deploy, debug, and observe physical AI apps from a local CLI and editor workflow.
      </p>
      <div className="mt-5 flex flex-wrap gap-x-6 gap-y-1 font-mono text-xs text-zinc-600">
        <span>Jetson Orin · AGX Thor</span>
        <span>Raspberry Pi 3 / 4 / 5</span>
        <span>Apache 2.0</span>
        <span>Swift · Python · Rust</span>
      </div>
    </div>
  );
}
