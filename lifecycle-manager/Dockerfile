# Cross-compile from the build platform: Go needs no emulation for arm64,
# so multi-arch builds never pay the QEMU tax.
FROM --platform=$BUILDPLATFORM golang:1.24 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/lifecycle-manager ./cmd/lifecycle-manager

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lifecycle-manager /lifecycle-manager
USER nonroot
ENTRYPOINT ["/lifecycle-manager"]
