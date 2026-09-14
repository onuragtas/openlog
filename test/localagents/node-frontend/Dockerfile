# Demo frontend (test/apmdemo/frontend: server.js, loadgen.js) instrumented with the openlog Node.js agent built from
# agents/node instead of a hand-written OpenTelemetry setup.
# Build contexts (test/localagents/docker-compose.yml): app = ../apmdemo/frontend, nodeagent = ../../agents/node.

FROM node:22-alpine AS agent
WORKDIR /agent
COPY --from=nodeagent package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY --from=nodeagent tsconfig.json LICENSE README.md ./
COPY --from=nodeagent scripts ./scripts
COPY --from=nodeagent src ./src
RUN node scripts/build.mjs && mkdir /out && npm pack --ignore-scripts --pack-destination /out

FROM node:22-alpine
ENV NODE_ENV=production
WORKDIR /app
COPY --from=agent /out/ /tmp/pkg/
# server.js uses @opentelemetry/api, api-logs and core directly: same versions as the agent (one shared API instance).
RUN npm init -y >/dev/null \
 && npm install --omit=dev --no-audit --no-fund /tmp/pkg/openlog-node-*.tgz express@5.2.1 \
      @opentelemetry/api@1.9.1 @opentelemetry/api-logs@0.222.0 @opentelemetry/core@2.11.0 \
 && npm cache clean --force && rm -rf /tmp/pkg
COPY --from=app server.js loadgen.js ./

USER node
EXPOSE 3000
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD wget -qO- http://127.0.0.1:3000/healthz >/dev/null || exit 1
CMD ["node", "--require", "openlog-node/register", "server.js"]
