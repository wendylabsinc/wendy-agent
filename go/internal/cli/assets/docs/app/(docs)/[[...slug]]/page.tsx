import { getMDXComponents } from '@/components/mdx';
import { source } from '@/lib/source';
import { ogImage, withBasePath } from '@/lib/shared';
import { DocsBody, DocsDescription, DocsPage, DocsTitle } from 'fumadocs-ui/layouts/docs/page';
import { createRelativeLink } from 'fumadocs-ui/mdx';
import type { Metadata } from 'next';
import { notFound, permanentRedirect } from 'next/navigation';

type PageParams = {
  slug?: string[];
};

type PageProps = {
  params: Promise<PageParams>;
};

// Pages with a bespoke OpenGraph card, keyed by page slug. Anything not listed
// here falls back to the shared `ogImage`.
const pageOgImages: Record<string, string> = {
  'installation/wendyos-nvidia-jetson-agx-thor': '/images/opengraph-thor.png',
  'integrations/ros2': '/images/opengraph-ros2.png',
  'guides/tutorials/mojo/hello-world': '/images/opengraph-mojo.png',
  'guides/tutorials/mojo/simple-web-server': '/images/opengraph-mojo.png',
  'guides/tutorials/mojo/max-graph': '/images/opengraph-max.png',
  'guides/tutorials/mojo/max-inference-service': '/images/opengraph-max.png',
};

function getLegacyRedirect(slug?: string[]) {
  const redirects: Record<string, string> = {
    'installation/wendyos-nvidia-jetson': '/installation/wendyos-nvidia-jetson-orin-nano/',
    'installation/wendy-agent-linux': '/installation/linux/',
    'installation/ubuntu': '/installation/linux/',
  };

  return redirects[slug?.join('/') ?? ''];
}

export default async function Page(props: PageProps) {
  const params = await props.params;
  const redirect = getLegacyRedirect(params.slug);
  if (redirect) {
    permanentRedirect(withBasePath(redirect));
  }

  const page = source.getPage(params.slug);
  if (!page) notFound();

  const MDX = page.data.body;

  return (
    <DocsPage toc={page.data.toc} full={page.data.full}>
      <DocsTitle>{page.data.title}</DocsTitle>
      <DocsDescription>{page.data.description}</DocsDescription>
      <DocsBody>
        <MDX
          components={getMDXComponents({
            a: createRelativeLink(source, page),
          })}
        />
      </DocsBody>
    </DocsPage>
  );
}

export function generateStaticParams() {
  return source.generateParams();
}

export async function generateMetadata(props: PageProps): Promise<Metadata> {
  const params = await props.params;
  const slug = params.slug?.join('/');
  const redirect = getLegacyRedirect(params.slug);
  if (redirect) {
    const isLegacyJetsonRoute = slug === 'installation/wendyos-nvidia-jetson';
    return {
      title: isLegacyJetsonRoute ? 'NVIDIA Jetson Orin Nano' : 'Linux',
      description: isLegacyJetsonRoute
        ? 'Install WendyOS on an NVIDIA Jetson Orin Nano.'
        : 'Install wendy-agent on a Linux machine.',
    };
  }

  const page = source.getPage(params.slug);
  if (!page) notFound();

  const pageOgImage = slug && slug in pageOgImages ? withBasePath(pageOgImages[slug]) : ogImage;

  return {
    title: page.data.title,
    description: page.data.description,
    openGraph: {
      images: pageOgImage,
    },
    twitter: {
      card: 'summary_large_image',
      images: pageOgImage,
    },
  };
}
