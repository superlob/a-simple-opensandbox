BIN=bin/runtime

.PHONY: build test test-race vet integration

build:
	go build -o $(BIN) ./cmd/runtime

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

integration:
	set -a; . ../SANDBOX.env; set +a; go test -tags integration ./internal/docker/... ./internal/execdcfg/... ./internal/sandbox/... ./test/integration/...
