# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION}" -o /out/dingzi-server ./cmd/server
RUN mkdir -p /out/data && chmod 0700 /out/data

FROM alpine:3.22.6@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Dingzi" \
      org.opencontainers.image.description="Self-hosted server monitoring panel" \
      org.opencontainers.image.source="https://github.com/oarw/dingzi" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/dingzi-server /usr/local/bin/dingzi-server
COPY --from=build --chown=10001:10001 /out/data /data
COPY LICENSE /usr/share/licenses/dingzi/LICENSE
USER 10001:10001
EXPOSE 8008
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -T 3 -O /dev/null http://127.0.0.1:8008/api/v1/session || exit 1
ENTRYPOINT ["/usr/local/bin/dingzi-server"]
CMD ["--data", "/data", "--listen", ":8008"]
