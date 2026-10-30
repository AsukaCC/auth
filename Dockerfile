FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS server
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -o /auth-server ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 auth \
    && adduser -S -D -H -u 10001 -G auth auth \
    && mkdir /data \
    && chown auth:auth /data \
    && chmod 700 /data
WORKDIR /app
COPY --from=server /auth-server /app/auth-server
COPY --from=web /app/web/dist /app/web/dist
USER 10001:10001
ENV AUTH_ADDR=0.0.0.0:8080 \
    AUTH_DATA_DIR=/data \
    AUTH_WEB_DIR=/app/web/dist \
    AUTH_PUBLIC_URL=http://127.0.0.1:8080 \
    AUTH_SECURE_COOKIE=false
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["wget", "-q", "-T", "3", "-O", "/dev/null", "http://127.0.0.1:8080/healthz"]
ENTRYPOINT ["/app/auth-server"]
