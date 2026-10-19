.PHONY: build test check fmt vet cover fuzz eval fixtures

build:
	go build -o sminer ./cmd/sminer

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
