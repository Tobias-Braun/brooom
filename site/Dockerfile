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
# Absolute URLs (canonical, Open Graph) are rendered at build time, so the
# public origin is baked into the image. The release workflow passes the
# repository variable SITE_URL; without it astro.config.mjs uses a placeholder.
ARG SITE_URL
RUN SITE_URL="$SITE_URL" npm run build

FROM nginxinc/nginx-unprivileged:1.28-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /home/node/site/dist /usr/share/nginx/html
EXPOSE 8080
