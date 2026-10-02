# Container image of the landing page, published to ghcr.io by
# .github/workflows/site.yml and deployed by the maintainer's infrastructure
# (FluxCD). The build stage renders the static Astro site; the runtime stage is
# an unprivileged nginx that only serves files, so the image runs as non-root
# on port 8080 and needs no writable filesystem beyond nginx's temp dirs.

FROM node:22-alpine AS build
# The build runs as the image's unprivileged user and only sees the files it
# needs: no recursive copy of the build context, and no npm install scripts
# (the build works without them, so a compromised dependency cannot run code
# at install time).
USER node
WORKDIR /home/node/site
COPY --chown=node:node package.json package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY --chown=node:node astro.config.mjs tsconfig.json ./
COPY --chown=node:node assets ./assets
COPY --chown=node:node public ./public
COPY --chown=node:node src ./src
# Absolute URLs (canonical, Open Graph) are rendered with a placeholder origin
# that nginx replaces with the SITE_URL env var at runtime (see nginx.conf).
RUN SITE_URL=https://site-url.invalid npm run build

FROM nginxinc/nginx-unprivileged:1.28-alpine
# The public origin of the site, e.g. https://brooom.dev. The nginx entrypoint
# renders the template into /tmp, so the root filesystem can stay read-only.
ENV SITE_URL=http://localhost:8080 \
    NGINX_ENVSUBST_OUTPUT_DIR=/tmp
COPY site-url.conf.template /etc/nginx/templates/
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /home/node/site/dist /usr/share/nginx/html
EXPOSE 8080
