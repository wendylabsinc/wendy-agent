import { source } from '@/lib/source';
import { ImageResponse } from 'next/og';
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
    slug: [...page.slugs, 'image.png'],
  }));
}

export async function GET(_request: Request, context: RouteProps) {
  const params = await context.params;
  const slug = params.slug?.slice(0, -1);
  const page = source.getPage(slug);
  if (!page) notFound();

  return new ImageResponse(
    (
      <div
        style={{
          alignItems: 'center',
          background: '#fff',
          color: '#111',
          display: 'flex',
          fontSize: 60,
          height: '100%',
          justifyContent: 'center',
          letterSpacing: 0,
          padding: 80,
          width: '100%',
        }}
      >
        {page.data.title}
      </div>
    ),
    {
      width: 1200,
      height: 630,
    },
  );
}
