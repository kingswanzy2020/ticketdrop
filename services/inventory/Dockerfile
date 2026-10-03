# syntax=docker/dockerfile:1
# Build context is the repository root: the service needs go.mod and pkg/.

# The build stage runs on the builder's own architecture and cross-compiles,
# so a multi-arch build needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS build
WORKDIR /src

# Dependencies first: this layer is reused until go.mod or go.sum changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Only what this service is built from, so a change to another service does
# not invalidate the layers below.
COPY pkg ./pkg
COPY services/inventory ./services/inventory

ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/kingswanzy2020/ticketdrop/pkg/platform/service.Version=$VERSION" \
      -o /out/service ./services/inventory/cmd

# No shell and no package manager; runs as UID 65532.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/service /service
EXPOSE 8080 9090
ENTRYPOINT ["/service"]
