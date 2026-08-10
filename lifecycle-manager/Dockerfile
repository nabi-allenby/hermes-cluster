FROM golang:1.24 AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/lifecycle-manager ./cmd/lifecycle-manager

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lifecycle-manager /lifecycle-manager
USER nonroot
ENTRYPOINT ["/lifecycle-manager"]
