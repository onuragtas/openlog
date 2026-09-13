# openlog PHP agent spike (option A) forwarder. Build context: agents/php/prototype-a/forwarder
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /forwarder .

FROM alpine:3.22
COPY --from=build /forwarder /usr/local/bin/forwarder
ENTRYPOINT ["/usr/local/bin/forwarder"]
