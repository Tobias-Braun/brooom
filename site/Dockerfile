# Container image of the landing page, published to ghcr.io by
# .github/workflows/site.yml and deployed by the maintainer's infrastructure
# (FluxCD). The build stage renders the static Astro site; the runtime stage is
# an unprivileged nginx that only serves files, so the image runs as non-root
# on port 8080 and needs no writable filesystem beyond nginx's temp dirs.

FROM node:22-alpine AS build
WORKDIR /site
# Absolute URLs (canonical, Open Graph) are rendered at build time, so the
# public origin is baked into the image. The release workflow passes the
# repository variable SITE_URL; without it astro.config.mjs uses a placeholder.
ARG SITE_URL
ENV SITE_URL=${SITE_URL}
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.28-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /site/dist /usr/share/nginx/html
EXPOSE 8080
