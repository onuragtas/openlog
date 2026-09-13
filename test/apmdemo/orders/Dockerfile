FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go build -ldflags="-s -w" -o /out/orders .

FROM alpine:3.22
RUN adduser -D -H -u 10001 app
COPY --from=build /out/orders /usr/local/bin/orders
USER app
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/orders"]
