# Console image: a Next.js standalone server on a slim Node base.
#
# Both stages are pinned by digest like every other image in this repo
# (docs/14-engineering-standards.md § Security). The console ships no secret: it is an
# API client, and the tenant's key never leaves the browser (ADR-012).
FROM node:22-bookworm-slim@sha256:48e4b67d85f87bd551df43704e24d252f56cc5f8e9718841aace50f19948f0f9 AS build

WORKDIR /app

# Dependencies first: a change to a component must not re-resolve the tree.
COPY console/package.json console/package-lock.json ./
RUN npm ci

COPY console/ ./
# The API origin is baked in at build time: Next inlines NEXT_PUBLIC_* into the bundle,
# so this is the one value the image is specific to. It is a public URL, not a secret.
ARG NEXT_PUBLIC_VITTS_API_URL=http://127.0.0.1:8080
ENV NEXT_PUBLIC_VITTS_API_URL=$NEXT_PUBLIC_VITTS_API_URL
RUN npm run build

FROM node:22-bookworm-slim@sha256:48e4b67d85f87bd551df43704e24d252f56cc5f8e9718841aace50f19948f0f9

ENV NODE_ENV=production
WORKDIR /app

# output: "standalone" traces only the modules the server actually loads, so the runtime
# layer carries neither the build toolchain nor the dev dependencies.
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
COPY --from=build --chown=node:node /app/public ./public

USER node
EXPOSE 3001
ENV PORT=3001 HOSTNAME=0.0.0.0

# No shell form: the process is pid 1 and receives signals directly, so a rolling deploy
# stops it rather than waiting for a timeout.
CMD ["node", "server.js"]
