.PHONY: build test cover bench bench-smoke fuzz lint vuln check-cmds doccheck docs docs-build release-check check fmt vet eval fixtures release-snapshot

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
DATE    ?= $(shell git log -1 --format=%cI 2>/dev/null)
LDFLAGS := -X github.com/rossedman/sminer/internal/version.Version=$(VERSION) -X github.com/rossedman/sminer/internal/version.Commit=$(COMMIT) -X github.com/rossedman/sminer/internal/version.Date=$(DATE)

GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
FUZZTIME    ?= 30s
COVER_MIN   ?= 90

build:
	go build -ldflags "$(LDFLAGS)" -o sminer ./cmd/sminer

test:
	go test -race ./...

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# Race tests with coverage across packages, gated at COVER_MIN% of
# hand-written code. tools/pkgcov.sh coverage.hand.out shows the breakdown.
cover:
	go test -race -coverpkg=./... -coverprofile=coverage.out ./...
	./tools/cover-gate.sh coverage.out $(COVER_MIN)

vuln:
	go run $(GOVULNCHECK) ./...

check-cmds:
	./tools/check-cmds.sh

doccheck:
	go run ./tools/doccheck ./...

# Saves results under bench/ so runs can be compared with benchstat.
bench:
	@mkdir -p bench
	go test -run '^$$' -bench . -benchmem -count 6 ./... | tee bench/$$(date +%Y%m%d-%H%M%S).txt

bench-smoke:
	go test -run '^$$' -bench . -benchtime 1x ./...

# Every fuzz target for FUZZTIME (default 30s) each.
fuzz:
	FUZZTIME=$(FUZZTIME) ./tools/fuzz.sh

docs:
	go run ./tools/gendocs

docs-build: docs
	mkdocs build --strict

release-check:
	goreleaser check

check: fmt vet lint cover check-cmds doccheck vuln bench-smoke fuzz docs-build release-check eval

eval:
	go run ./cmd/sminer eval --labels testdata/labels/candidates.jsonl

fixtures:
	python3 testdata/gen_fixtures.py

# Build every release archive into dist/ without publishing or signing
# (needs goreleaser).
release-snapshot:
	goreleaser release --snapshot --clean --skip=sign
