.PHONY: all build test test-integration lint lint-sh vuln dist clean

BIN       := sb
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

all: build test lint

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/sb

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./...

lint:
	golangci-lint run

lint-sh:
	shellcheck --shell=sh --severity=style install/install.sh

vuln:
	govulncheck ./...

# Cross-compile release archives and checksums into dist/.
dist:
	rm -rf dist && mkdir -p dist
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; name=sb_$(patsubst v%,%,$(VERSION))_$${os}_$${arch}; ext=; \
		if [ $$os = windows ]; then ext=.exe; fi; \
		echo "build $$name"; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/sb$$ext ./cmd/sb; \
		if [ $$os = windows ]; then (cd dist && zip -qr $$name.zip $$name); \
		else tar -C dist -czf dist/$$name.tar.gz $$name; fi; \
		rm -rf dist/$$name; \
	done
	cd dist && shasum -a 256 *.tar.gz *.zip > SHA256SUMS

clean:
	rm -rf $(BIN) dist
