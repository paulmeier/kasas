VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := bin/kasas
IMAGE   ?= kasas:latest

# Use the locally installed sqlc, falling back to `go run` if it isn't on PATH.
SQLC ?= $(shell command -v sqlc 2>/dev/null || echo go run github.com/sqlc-dev/sqlc/cmd/sqlc)

.PHONY: all
all: tidy generate build test

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: generate
generate: ## Regenerate sqlc code from queries/ and migrations/
	$(SQLC) generate

.PHONY: build
build: ## Build a static binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/kasas

.PHONY: run
run: ## Run the server locally (uses ./config.toml if present)
	go run ./cmd/kasas -config config.toml serve

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: test-race
test-race: ## Run the test suite with the race detector
	go test -race ./...

.PHONY: cover
cover: ## Run tests and open an HTML coverage report
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out

.PHONY: fmt
fmt: ## Format all Go code with gofmt
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt-formatted (what CI runs)
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then echo "Not gofmt-formatted:"; echo "$$files"; exit 1; fi; \
	echo "All Go files are gofmt-formatted."

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (install: https://golangci-lint.run)
	golangci-lint run ./...

.PHONY: docker
docker: ## Build the container image (linux/amd64, no attestation manifest)
	docker build --platform linux/amd64 --provenance=false \
		--build-arg VERSION=$(VERSION) -t $(IMAGE) .

.PHONY: clean
clean:
	rm -rf bin/

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'
