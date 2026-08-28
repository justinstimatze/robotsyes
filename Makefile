# Version is derived from the git tag — `git describe` gives v0.1.0 at the
# tag and v0.1.0-3-gabc1234 three commits later. There is no version
# constant to hand-edit; `git tag vX.Y.Z` is the single source of truth.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: install build test version vet fmt lint check

# Install to $GOBIN/$GOPATH/bin with the version baked in.
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/robotsyes

# Build a local ./robotsyes binary with the version baked in.
build:
	go build -ldflags "$(LDFLAGS)" -o robotsyes ./cmd/robotsyes

test:
	go test ./...

# Print the version that a build would stamp.
version:
	@echo $(VERSION)

vet:
	go vet ./...

# Fails (nonzero exit) on unformatted files, same check CI runs.
fmt:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt -l found unformatted files:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

lint:
	golangci-lint run

# The local gate — mirrors every job in .github/workflows/ci.yml, so a
# green check here means green CI. Keep this in lockstep with ci.yml.
check: vet fmt lint test
	@echo "All local gates passed (CI parity: vet + fmt + lint + test)."
