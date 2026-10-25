GO ?= go
GOVULNCHECK_VERSION := 1.9.0
export GOTOOLCHAIN := local

.PHONY: build verify toolchain-check format-check architecture modules vet test race security fuzz test-cli interop cross-build
.PHONY: release-candidate
# Local preparation only. Requires committed, clean inputs; never installs or publishes.
release-candidate: toolchain-check
	timeout 600 sh scripts/release-cli.sh "$(VERSION)"

build:
	$(GO) build -buildvcs=false ./...
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -o bin/tsuzuri ./cmd/tsuzuri

toolchain-check:
	test "$$($(GO) env GOVERSION)" = "go$$(cat .go-version)"
	test "$$($(GO) mod edit -json | sed -n 's/.*"Go": "\([^"]*\)".*/\1/p')" = "$$(cat .go-version)"

format-check:
	format_output="$$(git ls-files -z --cached --others --exclude-standard '*.go' | xargs -0 gofmt -l)" && test -z "$$format_output"

architecture:
	$(GO) run -buildvcs=false ./cmd/check-architecture

modules:
	$(GO) mod tidy -diff
	$(GO) mod verify

vet:
	$(GO) vet ./...
test:
	$(GO) test -count=1 -timeout 300s ./...
race:
	$(GO) test -race -count=1 -timeout 300s ./...

verify: toolchain-check format-check architecture modules vet test race build

# Requires the flake-pinned govulncheck and access to vuln.go.dev.
security:
	test "$$(govulncheck -version | sed -n 's/^Scanner: govulncheck@//p' | sed 's/^v//')" = "$(GOVULNCHECK_VERSION)"
	govulncheck ./...

# Seed corpus runs in ordinary tests; this target actually mutates inputs.
fuzz:
	$(GO) test -run '^$$' -fuzz '^FuzzCoordinationMetadata$$' -fuzztime=10s -parallel=2 -timeout 60s .
	$(GO) test -run '^$$' -fuzz '^FuzzTaskDocument$$' -fuzztime=10s -parallel=2 -timeout 60s .
	$(GO) test -run '^$$' -fuzz '^FuzzHistoryFraming$$' -fuzztime=10s -parallel=2 -timeout 60s .

test-cli:
	$(GO) test -count=1 -timeout 300s ./tests/cli

interop:
	test -n "$(TSUZURI_RUST_BINARY)"
	TSUZURI_RUST_BINARY="$(TSUZURI_RUST_BINARY)" $(GO) test -race -count=1 -timeout 300s ./...

cross-build:
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -buildvcs=false -trimpath -o bin/tsuzuri-$$os-$$arch ./cmd/tsuzuri || exit; \
	done
