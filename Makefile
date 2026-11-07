# agentstats — build, test, release.
# Requires Go 1.25+. No network is used at build or run time (stdlib only).

GO ?= go
BIN ?= bin/agentstats
VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt self-test doctor clean release dist

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
