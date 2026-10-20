.PHONY: build test check fmt vet cover fuzz eval fixtures release-snapshot

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
LDFLAGS := -X github.com/rossedman/sminer/internal/version.Version=$(VERSION) -X github.com/rossedman/sminer/internal/version.Commit=$(COMMIT)

build:
	go build -ldflags "$(LDFLAGS)" -o sminer ./cmd/sminer

test:
	go test ./...

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

check: fmt vet test

cover:
	go test -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fuzz:
	go test ./internal/transcript -run '^$$' -fuzz FuzzDecode -fuzztime 30s

eval:
	go run ./cmd/sminer eval --labels testdata/labels/candidates.jsonl

fixtures:
	python3 testdata/gen_fixtures.py

# Build every release archive into dist/ without publishing (needs goreleaser).
release-snapshot:
	goreleaser release --snapshot --clean
