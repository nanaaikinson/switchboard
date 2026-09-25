.PHONY: all build test test-integration lint vuln clean

BIN := sb

all: build test lint

build:
	go build -o $(BIN) ./cmd/sb

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

clean:
	rm -f $(BIN)
