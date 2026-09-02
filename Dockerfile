FROM node:22-alpine AS web-build

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26.7-alpine AS go-build

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-build /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w \
      -X github.com/hkjang/jupiq/internal/version.Version=${VERSION} \
      -X github.com/hkjang/jupiq/internal/version.Commit=${COMMIT} \
      -X github.com/hkjang/jupiq/internal/version.BuildTime=${BUILD_TIME}" \
    -o /out/jupiq ./cmd/jupiq

FROM alpine:3.22 AS runtime

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 jupiq \
    && adduser -S -D -H -u 10001 -G jupiq jupiq

WORKDIR /app
COPY --from=go-build --chown=10001:10001 /out/jupiq /app/jupiq
COPY --from=web-build --chown=10001:10001 /src/web/dist /app/web/dist
COPY --from=go-build --chown=10001:10001 /src/openapi /app/openapi

LABEL org.opencontainers.image.title="jupiq" \
      org.opencontainers.image.description="오프라인망용 AI Workspace Control Plane" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}" \
      org.opencontainers.image.source="https://github.com/hkjang/jupiq"

USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=5 \
  CMD wget -q -T 3 -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/jupiq"]
