import { basePath, withBasePath } from '@/lib/shared';

type Board = {
  name: string;
  tagline: string;
  logo: string;
  animation: string;
  href: string;
  features: string[];
};

const boards: Board[] = [
  {
    name: 'NVIDIA Jetson',
    tagline: 'Orin Nano, AGX Orin, AGX Thor',
    logo: '/icons/icons8-nvidia.svg',
    animation: '/images/boards/jetson-orin.webp',
    href: '/installation/wendyos-nvidia-jetson-orin-nano/',
    features: [
      'Up to 2000 TOPS AI performance',
      'CUDA, PyTorch & MLX support',
      'AGX Thor USB recovery flashing',
      'Hardware video encode & decode',
    ],
  },
  {
    name: 'Raspberry Pi',
    tagline: 'Pi 3, 4 & 5 (8GB Pi 5 recommended)',
    logo: '/icons/icons8-raspberry-pi.svg',
    animation: '/images/boards/raspberry-pi-5.webp',
    href: '/installation/wendyos-raspberry-pi-5/',
    features: [
      'Low power consumption',
      'Broad GPIO ecosystem',
      'Hardware PWM, SPI & I2C',
      'Affordable entry point',
    ],
  },
  {
    name: 'NVIDIA Jetson AGX Thor',
    tagline: 'High-performance physical AI at the edge',
    logo: '/icons/icons8-nvidia.svg',
    animation: '/images/boards/jetson-thor.gif',
    href: '/installation/wendyos-nvidia-jetson-agx-thor/',
    features: [
      'Up to 2000 TOPS AI performance',
      'CUDA, PyTorch & MLX support',
      'USB recovery flashing',
      'Hardware video encode & decode',
    ],
  },
  {
    name: 'Linux',
    tagline: 'Run Wendy on your Linux development machine',
    logo: '/icons/simple-icons-linux.svg',
    animation: '/images/boards/ubuntu.png',
    href: '/installation/linux/',
    features: [
      'Develop and deploy from your Linux machine',
      'Works with USB-C and LAN devices',
      'Full Wendy CLI support',
      'Ideal for robotics and edge AI workflows',
    ],
  },
];

export function HardwareShowcase() {
  return (
    <div className="not-prose my-8 grid gap-6 sm:grid-cols-2">
      {boards.map((board) => (
        <a
          key={board.name}
          href={withBasePath(board.href)}
          className="flex flex-col overflow-hidden border bg-fd-card transition-colors hover:border-fd-primary/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-fd-primary focus-visible:ring-offset-2 focus-visible:ring-offset-fd-background"
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
        </a>
      ))}
    </div>
  );
}
