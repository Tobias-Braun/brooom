/**
 * Joins a site-relative path onto Astro's configured base. `BASE_URL` may or
 * may not end in a slash depending on the Astro version and `trailingSlash`
 * setting, so it is normalised here once instead of in every component.
 */
export function withBase(path = ''): string {
  const base = import.meta.env.BASE_URL.replace(/\/+$/, '');
  return `${base}/${path.replace(/^\/+/, '')}`;
}

/** The GitHub repository every "source" link points to. */
export const REPO_URL = 'https://github.com/Tobias-Braun/brooom';

/** Files in the repository, addressed on the default branch. */
export function repoFile(path: string): string {
  return `${REPO_URL}/blob/main/${path}`;
}
