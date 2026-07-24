# Hatchway server — multi-stage build
# Stage 1: Build the Go binary
# Stage 2: Minimal runtime image, non-root

FROM golang:1.26.5-alpine AS builder

ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /hatchway ./cmd/hatchway

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /hatchway /hatchway
COPY LICENSE /usr/local/share/licenses/hatchway/LICENSE
COPY THIRD_PARTY_LICENSES /usr/local/share/licenses/hatchway/THIRD_PARTY_LICENSES

USER nonroot:nonroot

EXPOSE 9000 9001

HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=5 \
    CMD ["/hatchway", "server", "healthcheck", "--timeout", "3s"]

ENTRYPOINT ["/hatchway"]
