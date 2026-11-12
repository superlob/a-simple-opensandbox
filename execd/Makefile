BIN=bin/execd

.PHONY: build test test-race vet integration

build:
	go build -o $(BIN) ./cmd/execd

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

integration:
	go test -tags integration ./test/integration/...
