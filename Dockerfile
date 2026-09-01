# The probe image.
#
# Distroless and non-root because this runs inside a customer's cluster and a
# security reviewer will read the Dockerfile before the README. There is no
# shell, no package manager and nothing to escalate to: the image is one static
# binary and a CA bundle.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependencies first, so a code change does not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
# CGO off, so the binary is static and the runtime image needs no libc.
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/sonde ./cmd/sonde

# nonroot: uid 65532, and no shell to fall back to.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sonde /usr/local/bin/sonde

# The state directory holds the probe's signing key. The chart mounts a volume
# here; without one, an identity is lost on restart and the probe has to be
# enrolled again by a human.
VOLUME ["/var/lib/sonde"]

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/sonde"]
CMD ["probe"]
