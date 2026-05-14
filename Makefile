.PHONY: build test test-short coverage lint fmt tidy clean

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -ldflags "-X main.version=$(VERSION)"
BINARY   := dist/hatchway
COVERAGE := coverage.out

build:
	@mkdir -p dist
	go build $(LDFLAGS) -o $(BINARY) ./cmd/hatchway

# Run the full suite with atomic-mode coverage. Atomic is required for any
# package that uses goroutines (we have several) — `set` mode races.
test:
	go test -covermode=atomic -coverprofile=$(COVERAGE) ./...
	@go tool cover -func=$(COVERAGE) | tail -n 1

# Same, but skips integration tests (testcontainers, real Postgres). Mirrors CI's
# `go test -short` step.
test-short:
	go test -short -covermode=atomic -coverprofile=$(COVERAGE) ./...
	@go tool cover -func=$(COVERAGE) | tail -n 1

# Per-function coverage summary. Depends on `test` so it always reflects the
# current code; if you only want to read an existing profile, run `go tool
# cover -func=coverage.out` directly.
#
# Don't declare a file dependency on $(COVERAGE) — make's implicit `%.out: %`
# rule kicks in and tries to `cp coverage coverage.out`, which fails because
# `coverage` is a phony target with no source file.
coverage: test
	@go tool cover -func=$(COVERAGE)

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
	go mod tidy

tidy:
	go mod tidy

clean:
	rm -rf dist/ $(COVERAGE)
