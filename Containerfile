FROM golang:1.26-alpine AS builder
WORKDIR /src
ARG VERSION=dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/cloudflare-exporter ./cmd/exporter

FROM alpine:3.24
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 exporter
COPY --from=builder /out/cloudflare-exporter /usr/local/bin/cloudflare-exporter
USER exporter
EXPOSE 9199
ENTRYPOINT ["/usr/local/bin/cloudflare-exporter"]
CMD ["-config=/etc/cloudflare-exporter/config.yaml"]
