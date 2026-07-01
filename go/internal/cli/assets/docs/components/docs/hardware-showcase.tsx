import { basePath } from '@/lib/shared';

type Board = {
  name: string;
  tagline: string;
  logo: string;
  animation: string;
  features: string[];
};

const boards: Board[] = [
  {
    name: 'NVIDIA Jetson',
    tagline: 'Orin Nano, AGX Orin',
    logo: '/icons/icons8-nvidia.svg',
    animation: '/images/boards/jetson-orin.webp',
    features: [
      'Up to 2000 TOPS AI performance',
      'CUDA, PyTorch & MLX support',
      'Hardware video encode & decode',
      'Unified memory architecture',
    ],
  },
  {
    name: 'Raspberry Pi',
    tagline: 'Pi 3, 4 & 5 (8GB Pi 5 recommended)',
    logo: '/icons/icons8-raspberry-pi.svg',
    animation: '/images/boards/raspberry-pi-5.webp',
    features: [
      'Low power consumption',
      'Broad GPIO ecosystem',
      'Hardware PWM, SPI & I2C',
      'Affordable entry point',
    ],
  },
];

export function HardwareShowcase() {
  return (
    <div className="not-prose my-8 grid gap-6 sm:grid-cols-2">
      {boards.map((board) => (
        <div
          key={board.name}
          className="flex flex-col overflow-hidden border bg-fd-card transition-colors hover:border-fd-primary/50"
        >
          <img
            src={`${basePath}${board.animation}`}
            alt={`${board.name} board animation`}
            className="aspect-video w-full bg-fd-muted object-cover object-center"
            loading="lazy"
          />
          <div className="flex flex-1 flex-col gap-4 p-5">
            <div className="flex items-center gap-3">
              <img src={`${basePath}${board.logo}`} alt={board.name} className="h-9 w-9 object-contain" />
              <div>
                <h3 className="font-semibold text-fd-card-foreground">{board.name}</h3>
                <p className="text-sm text-fd-muted-foreground">{board.tagline}</p>
              </div>
            </div>
            <ul className="space-y-2 text-sm text-fd-muted-foreground">
              {board.features.map((feature) => (
                <li key={feature} className="flex items-start gap-2">
                  <span className="mt-1.5 inline-block h-1.5 w-1.5 shrink-0 bg-fd-primary" aria-hidden />
                  <span>{feature}</span>
                </li>
              ))}
            </ul>
          </div>
        </div>
      ))}
    </div>
  );
}
