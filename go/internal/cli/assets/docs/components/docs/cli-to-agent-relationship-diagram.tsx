export function CliToAgentRelationshipDiagram() {
  return (
    <figure className="my-6">
      <svg
        viewBox="0 0 640 240"
        role="img"
        aria-labelledby="cli-agent-diagram-title cli-agent-diagram-description"
        className="w-full border bg-white dark:bg-zinc-950"
      >
        <title id="cli-agent-diagram-title">Wendy CLI and wendy-agent relationship</title>
        <desc id="cli-agent-diagram-description">
          The Wendy CLI builds and sends app containers to wendy-agent, then wendy-agent runs them on the device.
        </desc>
        <defs>
          <marker
            id="arrow"
            markerHeight="10"
            markerWidth="10"
            orient="auto"
            refX="9"
            refY="3"
          >
            <path d="M0,0 L0,6 L9,3 z" className="fill-zinc-500 dark:fill-zinc-300" />
          </marker>
        </defs>
        <rect
          x="40"
          y="52"
          width="220"
          height="136"
          className="fill-zinc-100 stroke-zinc-300 dark:fill-zinc-900 dark:stroke-zinc-700"
        />
        <text x="150" y="92" textAnchor="middle" className="fill-zinc-950 dark:fill-zinc-50" fontSize="20" fontWeight="700">
          Developer Machine
        </text>
        <text x="150" y="124" textAnchor="middle" className="fill-zinc-600 dark:fill-zinc-300" fontSize="16">
          Wendy CLI
        </text>
        <text x="150" y="148" textAnchor="middle" className="fill-zinc-600 dark:fill-zinc-300" fontSize="14">
          build, discover, deploy
        </text>
        <line
          x1="280"
          y1="110"
          x2="360"
          y2="110"
          strokeWidth="4"
          markerEnd="url(#arrow)"
          className="stroke-zinc-500 dark:stroke-zinc-300"
        />
        <text x="320" y="94" textAnchor="middle" className="fill-zinc-600 dark:fill-zinc-300" fontSize="13">
          app + logs
        </text>
        <rect
          x="380"
          y="52"
          width="220"
          height="136"
          className="fill-zinc-100 stroke-zinc-300 dark:fill-zinc-900 dark:stroke-zinc-700"
        />
        <text x="490" y="92" textAnchor="middle" className="fill-zinc-950 dark:fill-zinc-50" fontSize="20" fontWeight="700">
          Target Device
        </text>
        <text x="490" y="124" textAnchor="middle" className="fill-zinc-600 dark:fill-zinc-300" fontSize="16">
          wendy-agent
        </text>
        <text x="490" y="148" textAnchor="middle" className="fill-zinc-600 dark:fill-zinc-300" fontSize="14">
          containers, hardware,
        </text>
        <text x="490" y="168" textAnchor="middle" className="fill-zinc-600 dark:fill-zinc-300" fontSize="14">
          telemetry
        </text>
      </svg>
    </figure>
  );
}
