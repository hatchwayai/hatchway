# Hatchway server — multi-stage build
# Stage 1: Build the Go binary
# Stage 2: Minimal runtime image, non-root

FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /hatchway ./cmd/hatchway

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /hatchway /hatchway

USER nonroot:nonroot

EXPOSE 9000 9001

ENTRYPOINT ["/hatchway"]
