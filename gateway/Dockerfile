# Gateway image: a static binary on a distroless base.
#
# Both stages are pinned by digest, so a rebuild months from now produces the same image
# rather than whatever the tag points at then (docs/14-engineering-standards.md).
#
# The build stage tracks a current Go patch release rather than the module's minimum:
# govulncheck reports standard-library advisories against whatever toolchain produced the
# binary, so an old builder ships known-vulnerable stdlib code even when the source is
# unchanged. go.mod's `go` directive stays at the lower minimum for consumers.
FROM golang:1.26-bookworm@sha256:a688600ca24f8a4d3ca77f95b0dd40704a9fc787c826660eb7ba0b641b8b175d AS build

WORKDIR /src

# Dependencies first: a code change must not re-download the module cache.
COPY gateway/go.mod gateway/go.sum ./
RUN go mod download

COPY gateway/ ./
# CGO off so the binary runs on a base with no libc. -trimpath keeps build paths out of
# the binary; the ldflags drop the symbol table and DWARF, which shrink it and remove
# detail an attacker would otherwise get for free.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/gateway ./cmd/gateway

# distroless/static has no shell, no package manager and no libc: there is nothing to
# exec even if a bug let someone try.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/gateway /gateway

# nonroot is uid 65532 in this base.
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/gateway"]
