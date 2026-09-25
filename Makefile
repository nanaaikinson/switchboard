.PHONY: all build test test-integration test-e2e-linux test-ui ui lint lint-sh vuln dist clean

BIN       := sb
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

all: build test lint

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/sb

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./...

lint:
	golangci-lint run

# Rebuild the dashboard into ui/dashboard/dist, which sb embeds. Commit the
# result; CI fails if it doesn't match the sources.
ui:
	cd ui/dashboard && npm ci && npm run build

# Playwright smoke tests of the dashboard against a fake API.
test-ui:
	cd ui/dashboard && npm ci && npx playwright install chromium && npm test

lint-sh:
	shellcheck --shell=sh --severity=style install/install.sh install/packaging/*.sh test/e2e/linux/*.sh .github/scripts/*.sh test/release/*.sh
	sh test/release/next-version-test.sh >/dev/null

vuln:
	govulncheck ./...

# Build release archives, SHA256SUMS and .deb/.rpm packages into dist/ with
# GoReleaser (https://goreleaser.com/install), without publishing.
dist:
	goreleaser release --snapshot --clean --skip=publish

# End-to-end test on Linux in a privileged container that boots systemd.
# Needs Docker; never uses sudo on this machine. See test/e2e/linux.
test-e2e-linux:
	./test/e2e/linux/e2e.sh

clean:
	rm -rf $(BIN) dist
