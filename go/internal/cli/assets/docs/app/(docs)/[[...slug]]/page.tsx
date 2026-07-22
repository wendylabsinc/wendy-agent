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

  const pageOgImage =
    slug === 'installation/wendyos-nvidia-jetson-agx-thor'
      ? withBasePath('/images/opengraph-thor.png')
      : ogImage;

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
