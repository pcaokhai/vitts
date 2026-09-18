# Static, non-root gateway image. Digest pinning and further hardening are task 3.3.
FROM golang:1.25-bookworm AS build

WORKDIR /src

# Dependencies first: a code change must not re-download the module cache.
COPY gateway/go.mod gateway/go.sum ./
RUN go mod download

COPY gateway/ ./
# CGO off so the binary runs on a distroless base with no libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /gateway
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/gateway"]
