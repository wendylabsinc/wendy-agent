import { getLLMText, getPageMarkdownUrl, source } from '@/lib/source';
import { notFound } from 'next/navigation';

export const revalidate = false;

type RouteParams = {
  slug?: string[];
};

type RouteProps = {
  params: Promise<RouteParams>;
};

export function generateStaticParams() {
  return source.getPages().map((page) => ({
    slug: getPageMarkdownUrl(page).segments,
  }));
}

export async function GET(_request: Request, context: RouteProps) {
  const params = await context.params;
  const slug = params.slug?.slice(0, -1);
  const page = source.getPage(slug);
  if (!page) notFound();

  return new Response(await getLLMText(page), {
    headers: {
      'Content-Type': 'text/markdown',
    },
  });
}
