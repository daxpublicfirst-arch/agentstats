# agentstats — build, test, release.
# Requires Go 1.25+. No network is used at build or run time (stdlib only).

GO ?= go
BIN ?= bin/agentstats
VERSION ?= 0.1.1
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt self-test doctor clean release dist test-arm64 bench

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/agentstats

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l .

self-test: build
	./$(BIN) self-test

doctor: build
	./$(BIN) doctor

check: fmt vet test self-test doctor

bench:
	$(GO) test -run=NONE -bench=BenchmarkSession50MB -benchtime=1x ./internal/transcript/

# Run the arm64 binary under qemu (proves it executes, not just builds).
# Skips gracefully when qemu-aarch64-static is not installed.
test-arm64: release
	@if ! command -v qemu-aarch64-static >/dev/null 2>&1; then echo "qemu-aarch64-static not installed, skipping test-arm64"; exit 0; fi
	qemu-aarch64-static dist/agentstats-linux-arm64 version
	qemu-aarch64-static dist/agentstats-linux-arm64 doctor
	qemu-aarch64-static dist/agentstats-linux-arm64 self-test

clean:
	rm -rf bin dist

# Static binaries for linux-amd64 and linux-arm64, plus tarballs and checksums.
release: clean
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/agentstats-linux-amd64 ./cmd/agentstats
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/agentstats-linux-arm64 ./cmd/agentstats
	cp plugin.toml dist/
	tar -C dist -czf dist/agentstats-$(VERSION)-linux-amd64.tar.gz agentstats-linux-amd64 plugin.toml
	tar -C dist -czf dist/agentstats-$(VERSION)-linux-arm64.tar.gz agentstats-linux-arm64 plugin.toml
	cd dist && sha256sum agentstats-linux-amd64 agentstats-linux-arm64 agentstats-$(VERSION)-linux-amd64.tar.gz agentstats-$(VERSION)-linux-arm64.tar.gz > SHA256SUMS
	ls -l dist
