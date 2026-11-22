# Build stage: compile a static Go binary.
# --platform=$BUILDPLATFORM: compile natively and cross-compile for each
# target, instead of emulating arm64 (much faster multi-arch builds).
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src

# Download dependencies first, in their own layer, so code changes don't
# re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
# Set by buildx for each target platform. No defaults here: a default
# overrides buildx's value, and every platform would get an amd64 binary.
ARG TARGETOS
ARG TARGETARCH
# CGO_ENABLED=0: no C libraries needed, so the binary runs on a minimal image.
# -trimpath and -s -w: reproducible, smaller binary.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" -o /out/sophron-collector ./cmd/collector

# Run stage: distroless has no shell or package manager, only our binary.
# Smaller image, fewer vulnerabilities. "nonroot" runs as user 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sophron-collector /sophron-collector
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/sophron-collector"]
