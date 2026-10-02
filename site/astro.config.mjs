import { readdir, readFile, rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { defineConfig } from 'astro/config';
import vue from '@astrojs/vue';

/** Recursively lists all files below a directory. */
async function listFiles(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(
    entries.map((e) => (e.isDirectory() ? listFiles(path.join(dir, e.name)) : [path.join(dir, e.name)])),
  );
  return nested.flat();
}

/**
 * `@astrojs/vue` always emits its client runtime (`_astro/client.*.js`), even
 * when no page hydrates a Vue component. Until the islands of #43 exist the
 * page must ship zero JavaScript, so this integration deletes every emitted
 * script that no HTML file references. Once a page uses an island its
 * scripts are referenced and therefore kept, so nothing needs to change then.
 */
function pruneUnusedScripts() {
  return {
    name: 'prune-unused-scripts',
    hooks: {
      'astro:build:done': async ({ dir }) => {
        const root = fileURLToPath(dir);
        const files = await listFiles(root);
        const scripts = files.filter((f) => f.endsWith('.js'));
        // Reachability: start from what the HTML pages mention, then follow
        // imports between scripts so shared chunks of a used island survive.
        const kept = new Set();
        let queue = await Promise.all(files.filter((f) => f.endsWith('.html')).map((f) => readFile(f, 'utf8')));
        while (queue.length > 0) {
          const found = scripts.filter((f) => !kept.has(f) && queue.some((text) => text.includes(path.basename(f))));
          found.forEach((f) => kept.add(f));
          queue = await Promise.all(found.map((f) => readFile(f, 'utf8')));
        }
        await Promise.all(scripts.filter((f) => !kept.has(f)).map((f) => rm(f)));
      },
    },
  };
}

// The site is served at the root of its own origin by the site image (see
// site/Dockerfile). The origin only matters for absolute URLs (canonical, Open
// Graph) and comes from SITE_URL; local builds use the dev server origin. The
// image builds with a placeholder that nginx swaps for the container's SITE_URL.
const siteURL = process.env.SITE_URL || 'http://localhost:4321';

export default defineConfig({
  site: siteURL,
  base: '/',
  output: 'static',
  // Vue powers only the interactive islands (preset tabs, terminal demo,
  // install tabs, copy buttons); every other section is static HTML.
  integrations: [vue(), pruneUnusedScripts()],
});
