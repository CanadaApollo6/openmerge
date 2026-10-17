TOOLCHAIN := $(if $(shell command -v go 2>/dev/null),,mise exec --)
VERSION ?= $(shell git describe --tags --always --dirty)

.PHONY: build test test-sim-exits test-sim-adaptive-full lint fmt check ci clean db db-stop

build:
	CGO_ENABLED=0 $(TOOLCHAIN) go build -ldflags "-X main.version=$(VERSION)" -o bin/openmerge ./cmd/openmerge

test:
	$(TOOLCHAIN) go test -race -short -count=1 -timeout=15m ./...

# Required deterministic simulator workloads; the full adaptive exit is separate.
test-sim-exits:
	$(TOOLCHAIN) go test -count=1 -timeout=60m -parallel=4 -v ./internal/sim

# Non-required pinned adaptive exit and paired fixed-size controls.
test-sim-adaptive-full:
	$(TOOLCHAIN) go test -count=1 -timeout=60m -parallel=4 -v ./internal/sim -run '^TestExitAdopterAdaptive$$' -sim-adaptive-full-exit

lint:
	$(TOOLCHAIN) golangci-lint run

fmt:
	@files="$$($(TOOLCHAIN) gofmt -l .)" || exit $$?; \
	if [ -n "$$files" ]; then \
		printf 'Run gofmt on these files:\n%s\n' "$$files"; \
		exit 1; \
	fi

check: fmt lint test build

# Go checks from both required CI jobs: check and sim-exits.
ci: check test-sim-exits

clean:
	rm -rf bin/

db:
	@sh scripts/dev-db.sh start

db-stop:
	@sh scripts/dev-db.sh stop
