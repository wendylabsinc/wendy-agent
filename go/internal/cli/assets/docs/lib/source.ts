import { docs } from 'collections/server';
import { loader } from 'fumadocs-core/source';
import { docsRoute } from './shared';

export const source = loader({
  baseUrl: docsRoute,
  source: docs.toFumadocsSource(),
  plugins: [],
});
