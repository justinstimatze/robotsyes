# syntax=docker/dockerfile:1

# ---- build ----
FROM golang:1.27-alpine AS builder
WORKDIR /src

# Cache module downloads in their own layer, invalidated only by go.mod/go.sum.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# TAGS is empty for the default image (content negotiation + bulk export
# only) or "payments,torrent" for the full image — the same split
# Makefile's build/build-full targets use (see Makefile, FULL_TAGS).
ARG TAGS=""
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -tags "${TAGS}" -ldflags "-s -w -X main.version=${VERSION}" -o /out/robotsyes ./cmd/robotsyes

# ---- runtime ----
FROM alpine:3.20

# ca-certificates: robotsyes makes outbound HTTPS calls (identity card
# fetches, x402 settlement in the full image) that need a trust store.
RUN apk add --no-cache ca-certificates && \
    addgroup -S robotsyes && adduser -S robotsyes -G robotsyes

COPY --from=builder /out/robotsyes /usr/local/bin/robotsyes

USER robotsyes
EXPOSE 8080

# No config is baked in — origin is required and there's nothing sane to
# default it to inside a container. Mount one at this path:
#   docker run -v $(pwd)/robotsyes.yaml:/etc/robotsyes/robotsyes.yaml:ro ...
# Omitting the mount fails fast with a clear error instead of silently
# listening against an unreachable localhost origin.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -q -O- http://localhost:8080/.well-known/robots-yes.json || exit 1

ENTRYPOINT ["robotsyes"]
CMD ["serve", "-config", "/etc/robotsyes/robotsyes.yaml"]
