# Multi-stage build for whatsapp-http

# 1) Builder
FROM golang:1.24 AS builder
WORKDIR /src

# Improve layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source
COPY . .

# Build statically (no CGO) for Linux
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w" -o /out/whatsapp-http .

# 2) Runtime
FROM alpine:3.20
# Add CA certs for HTTPS calls made by whatsmeow
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S -G app app

# Runtime dirs
WORKDIR /app
RUN mkdir -p /data && chown -R app:app /data

# Copy binary
COPY --from=builder /out/whatsapp-http /usr/local/bin/whatsapp-http

# Default envs (can be overridden at runtime)
ENV ADDR=":8080"
# Persist the WhatsApp/whatsmeow sqlite DB under /data by default
ENV WHATSAPP_SQLITE_DSN="file:/data/whatsmeow.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"

EXPOSE 8080
VOLUME ["/data"]

USER app

ENTRYPOINT ["/usr/local/bin/whatsapp-http"]
