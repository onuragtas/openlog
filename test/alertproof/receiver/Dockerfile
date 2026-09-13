# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /alert-receiver .

FROM alpine:3.22
RUN apk add --no-cache wget
COPY --from=build /alert-receiver /usr/local/bin/alert-receiver
EXPOSE 8080 8443
ENTRYPOINT ["/usr/local/bin/alert-receiver"]
