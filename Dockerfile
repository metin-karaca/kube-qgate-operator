# Build the manager binary.
#
# The builder deliberately runs on the *build* platform rather than the target one. Without
# --platform, a multi-arch build runs this entire stage under QEMU emulation for every foreign
# architecture, and emulating a full Go toolchain turns a one-minute build into a 30-minute one.
# Pinning it to BUILDPLATFORM lets Go cross-compile natively instead, which it does for free with
# CGO disabled - that is what TARGETOS/TARGETARCH below are for.
FROM --platform=${BUILDPLATFORM} golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# TARGETARCH has no default so that a plain "docker build" still produces a binary matching the
# host, while a buildx multi-arch build cross-compiles one binary per requested platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
