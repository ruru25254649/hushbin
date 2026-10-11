# syntax=docker/dockerfile:1

# ---- frontend -------------------------------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- backend --------------------------------------------------------------
FROM golang:1.24-alpine AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY server ./server
COPY web/embed.go ./web/embed.go
COPY --from=web /src/web/dist ./web/dist
# Set by CI (release tag or dev-<commit>); empty keeps the version in config.go.
ARG VERSION=""
RUN go build -trimpath -ldflags="-s -w ${VERSION:+-X hushbin/server/config.Version=$VERSION}" -o /out/hushbin ./cmd/hushbin \
 && mkdir -p /out/data

# ---- runtime --------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hushbin /hushbin
# Owned by the distroless "nonroot" user (65532) so a fresh named volume
# mounted at /data inherits a writable directory.
COPY --from=build --chown=65532:65532 /out/data /data
ENV HB_ADDR=:8080 \
    HB_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER 65532:65532
# distroless has no shell or curl: the binary probes itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/hushbin", "healthcheck"]
ENTRYPOINT ["/hushbin"]
