VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := bin/kasas
IMAGE   ?= kasas:latest
SEED_EXTRA ?= 0

# Dev server port for `make run` / `make kill-port`. Empty = auto-detect from
# $KASAS_SERVER_ADDR or [server].addr in config.toml (falling back to 8080).
# Override explicitly with `make run PORT=9000`.
PORT ?=

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

.PHONY: wasm
wasm: ## Build + gzip the dashboard WebAssembly client (embedded by the server)
	GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" \
		-o internal/dashboard/web/app.wasm ./cmd/kasas-wasm
	gzip -9 -f internal/dashboard/web/app.wasm

.PHONY: build
build: wasm ## Build a static binary into bin/ (builds the WASM first)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/kasas

.PHONY: run
run: wasm kill-port ## Run the server locally (frees the port first; uses ./config.toml)
	go run ./cmd/kasas -config config.toml serve

.PHONY: kill-port
kill-port: ## Free the dev server port by killing whatever is listening on it
	@port="$(PORT)"; \
	if [ -z "$$port" ]; then \
		addr="$${KASAS_SERVER_ADDR:-$$(sed -n 's/^[[:space:]]*addr[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' config.toml 2>/dev/null | head -n1)}"; \
		port="$${addr##*:}"; \
	fi; \
	port="$${port:-8080}"; \
	pids="$$(lsof -ti tcp:$$port -sTCP:LISTEN 2>/dev/null || true)"; \
	if [ -z "$$pids" ]; then \
		echo "kill-port: port $$port already free"; \
	else \
		echo "kill-port: freeing port $$port (PID(s): $$pids)"; \
		kill $$pids 2>/dev/null || true; \
		sleep 1; \
		pids="$$(lsof -ti tcp:$$port -sTCP:LISTEN 2>/dev/null || true)"; \
		if [ -n "$$pids" ]; then \
			echo "kill-port: still listening, sending SIGKILL ($$pids)"; \
			kill -9 $$pids 2>/dev/null || true; \
			sleep 1; \
		fi; \
	fi

.PHONY: seed
seed: ## Seed the configured DB with demo data (re-runnable; SEED_EXTRA=N for more)
	go run ./scripts/seed -config config.toml -extra $(SEED_EXTRA)

.PHONY: seed-reset
seed-reset: ## Wipe ./data and re-seed from a clean slate (local SQLite dev)
	rm -rf ./data
	$(MAKE) seed

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
