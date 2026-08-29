# Version is derived from the git tag — `git describe` gives v0.1.0 at the
# tag and v0.1.0-3-gabc1234 three commits later. There is no version
# constant to hand-edit; `git tag vX.Y.Z` is the single source of truth.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Payments (x402) and torrent (BEP-19) are behind build tags, not just
# config flags: cmd/robotsyes/payments_stub.go and
# internal/export/torrent_stub.go mean a plain `go install`/`make
# install` never links in chit or anacrolix/torrent at all. Pass
# FULL_TAGS to the *-full targets to build/test/lint the version that
# does.
FULL_TAGS := payments,torrent

.PHONY: install build test version vet fmt lint check \
	install-full build-full test-full lint-full check-full

# Install to $GOBIN/$GOPATH/bin with the version baked in.
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/robotsyes

# Same, with payments and torrent support compiled in.
install-full:
	go install -tags $(FULL_TAGS) -ldflags "$(LDFLAGS)" ./cmd/robotsyes

# Build a local ./robotsyes binary with the version baked in.
build:
	go build -ldflags "$(LDFLAGS)" -o robotsyes ./cmd/robotsyes

# Same, with payments and torrent support compiled in.
build-full:
	go build -tags $(FULL_TAGS) -ldflags "$(LDFLAGS)" -o robotsyes ./cmd/robotsyes

test:
	go test ./...

# Exercises chitgate's proxy integration and torrent.go — code the
# default `test` target's build tags exclude entirely.
test-full:
	go test -tags $(FULL_TAGS) ./...

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

lint-full:
	golangci-lint run --build-tags=$(FULL_TAGS)

# The local gate — mirrors every job in .github/workflows/ci.yml, so a
# green check here means green CI. Keep this in lockstep with ci.yml.
check: vet fmt lint test
	@echo "All local gates passed (CI parity: vet + fmt + lint + test)."

# Same, against the payments+torrent build. Run both check and
# check-full before a release — they cover different code.
check-full: vet fmt lint-full test-full
	@echo "All local gates passed for the payments+torrent build."
