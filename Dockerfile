# --- Build stage (Alpine) ---
FROM golang:1.25-alpine AS builder-alpine
WORKDIR /app
COPY src/ .
RUN go mod tidy
RUN go build -ldflags="-s -w" -o main .

# --- Build stage (Debian-slim) ---
FROM bitnami/golang:1.25-debian-13 AS builder-slim
WORKDIR /app
COPY src/ .
RUN go mod tidy
RUN go build -ldflags="-s -w" -o main .

# --- Final image (Alpine) ---
FROM alpine:3 AS final-alpine
WORKDIR /app
COPY --from=builder-alpine /app/main ./main
RUN apk add --no-cache ca-certificates && \
    rm -rf /var/cache/apk/*
CMD ["./main"]

# --- Final image (Debian-slim) ---
FROM debian:stable-slim AS final-slim
WORKDIR /app
COPY --from=builder-slim /app/main ./main
RUN apt-get update && \
    apt-get install -y ca-certificates && \
    apt-get clean && \
    apt-get autoremove -y && \
    rm -rf /var/lib/apt/lists/*
CMD ["./main"]