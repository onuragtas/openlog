# Static openlog-infra-agent image. The build context is the repository root, because the agent
# depends on libs/release through a go.mod replace directive:
#
#   docker build -f agents/infra/Dockerfile -t openlog/openlog-infra-agent .     # from the repo root
#   make -C agents/infra docker
#
# Run with the host root mounted at /host:
#
#   docker run -d --name openlog-infra-agent --pid=host --network=host \
#     -v /:/host:ro -v openlog-agent-state:/var/lib/openlog-infra-agent \
#     --cap-add SYS_PTRACE --cap-add DAC_READ_SEARCH \
#     -e OPENLOG_LICENSE_KEY=... -e OPENLOG_ENDPOINT=https://ingest.example:4318 \
#     openlog/openlog-infra-agent
#
# Containers never update themselves (update_capable=false): update the image tag instead.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=0.0.0-dev
ARG COMMIT=
ARG DATE=
ARG OPENLOG_RELEASE_PUBLIC_KEYS=
WORKDIR /src/agents/infra
COPY libs/release /src/libs/release
COPY agents/infra/go.mod agents/infra/go.sum ./
RUN go mod download
COPY agents/infra ./
RUN M=github.com/onuragtas/openlog/agents/infra/internal && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w \
      -X $M/version.Version=${VERSION} -X $M/version.Commit=${COMMIT} -X $M/version.Date=${DATE} \
      -X $M/release.trustedKeys=${OPENLOG_RELEASE_PUBLIC_KEYS}" \
    -o /out/openlog-infra-agent ./cmd/openlog-infra-agent

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/openlog-infra-agent /usr/bin/openlog-infra-agent
COPY agents/infra/packaging/container.yaml /etc/openlog-infra-agent/container.yaml
ENV OPENLOG_AGENT_CONTAINER=1
VOLUME ["/var/lib/openlog-infra-agent"]
ENTRYPOINT ["/usr/bin/openlog-infra-agent", "-config", "/etc/openlog-infra-agent/container.yaml"]
