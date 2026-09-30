/**
 * Joins a site-relative path onto Astro's configured base. `BASE_URL` may or
 * may not end in a slash depending on the Astro version and `trailingSlash`
 * setting, so it is normalised here once instead of in every component.
 */
export function withBase(path = ''): string {
  return `${trimSlashes(import.meta.env.BASE_URL, 'end')}/${trimSlashes(path, 'start')}`;
}

/**
 * Removes slashes from one end of a string with a plain scan. This avoids a
 * backtracking regular expression on input that is not fully under our
 * control.
 */
function trimSlashes(value: string, side: 'start' | 'end'): string {
  let start = 0;
  let end = value.length;
  if (side === 'start') {
    while (start < end && value[start] === '/') start++;
  } else {
    while (end > start && value[end - 1] === '/') end--;
  }
  return value.slice(start, end);
}

/** The GitHub repository every "source" link points to. */
export const REPO_URL = 'https://github.com/Tobias-Braun/brooom';

/** Files in the repository, addressed on the default branch. */
export function repoFile(path: string): string {
  return `${REPO_URL}/blob/main/${path}`;
}
