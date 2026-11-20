# syntax=docker/dockerfile:1.6
# The toolchain runs natively ($BUILDPLATFORM) and cross-compiles to the
# platform buildx sets in TARGETOS/TARGETARCH. Those ARGs must have no default:
# a default overrides buildx's value and builds every platform for that arch.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

ARG TARGETOS
ARG TARGETARCH

# Version stamps (the release workflow sets these from the git tag).
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# :? fails a build that sets no platform (e.g. without BuildKit) instead of building the host's.
RUN : "${TARGETOS:?}" "${TARGETARCH:?}" && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w \
        -X main.version=${VERSION} \
        -X main.commit=${COMMIT} \
        -X main.date=${BUILD_DATE}" \
      -o /workspace/manager ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
