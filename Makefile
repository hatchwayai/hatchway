.PHONY: build test test-short coverage lint lint-docs fmt fmt-check tidy validate-config validate-release clean

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -ldflags "-X main.version=$(VERSION)"
BINARY   := dist/hatchway
COVERAGE := coverage.out
GORELEASER_VERSION ?= v2.17.0
GORELEASER := go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

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
	$(GOLANGCI_LINT) run ./...

lint-docs:
	npx --yes markdownlint-cli2@0.23.1 README.md DESIGN.md PLAN.md docs/*.md

fmt:
	find . -type f -name '*.go' -not -path './vendor/*' -exec gofmt -w {} +

fmt-check:
	@unformatted="$$(find . -type f -name '*.go' -not -path './vendor/*' -exec gofmt -l {} +)"; \
	if [ -n "$$unformatted" ]; then \
		echo "Go files need formatting:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

tidy:
	go mod tidy

validate-config:
	bash -n scripts/download-frp.sh scripts/smoke.sh
	sh -n scripts/frps-entrypoint.sh
	docker compose config --quiet

validate-release:
	./scripts/download-frp.sh
	$(GORELEASER) check
	$(GORELEASER) release --snapshot --clean
	@set -eu; count=0; \
	for archive in dist/hatchway_*.tar.gz; do \
		[ -f "$$archive" ] || continue; \
		count=$$((count + 1)); \
		if ! tar -tzf "$$archive" | grep -Eq '(^|/)frpc$$'; then \
			echo "client archive is missing frpc: $$archive" >&2; \
			exit 1; \
		fi; \
		mode="$$(tar -tvzf "$$archive" | awk '/\/frpc$$/ { print $$1 }')"; \
		case "$$mode" in -rwx*) ;; \
			*) echo "client archive frpc is not executable: $$archive ($$mode)" >&2; exit 1 ;; \
		esac; \
	done; \
	if [ "$$count" -ne 4 ]; then \
		echo "expected 4 client archives, found $$count" >&2; \
		exit 1; \
	fi; \
	if ! file dist/hatchway_linux_amd64_v1/hatchway | grep -q "statically linked"; then \
		echo "Linux release binary is not statically linked" >&2; \
		exit 1; \
	fi

clean:
	rm -rf dist/ .release/ $(COVERAGE)
