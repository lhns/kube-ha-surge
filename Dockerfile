# The compiler runs natively ($BUILDPLATFORM) and cross-compiles, so no QEMU is needed (the
# runtime stage has no RUN). The golang tag is go.mod's toolchain version; CI fails on a minor
# mismatch, and the local setup-go action makes CI test with this exact Go.
FROM --platform=$BUILDPLATFORM golang:1.26.11@sha256:4165ee6bf3b7811fd7b40373290405c83d0167dcb87ee5f29c427cdcef45ff8f AS builder
# PER-REPO: the line above (golang:<go.mod toolchain version>@sha256:<digest>).

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

# PER-REPO: the source directories the build needs (traefik-reflector and ha-surge have no api/).
COPY cmd/ cmd/
COPY internal/ internal/

# No defaults: a default overrides the per-platform value buildx sets, which once built every
# platform for amd64. The :? guard fails a build that sets none (no BuildKit).
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
# PER-REPO: oci-composer adds `ARG CMD=oci-composer` and builds ./cmd/${CMD}.

RUN : "${TARGETOS:?}" "${TARGETARCH:?}" && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
      -o /workspace/manager ./cmd

FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931

WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
