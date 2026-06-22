# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/loki-syslog-exporter ./cmd/loki-syslog-exporter

FROM alpine:3.24
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 65532 app \
    && mkdir -p /data \
    && chown app:app /data
COPY --from=build /out/loki-syslog-exporter /usr/local/bin/loki-syslog-exporter
USER app
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/loki-syslog-exporter"]
