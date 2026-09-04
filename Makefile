BIN := bin/brd
PREFIX ?= $(HOME)/.local

.PHONY: build install test race vet fmt clean

build:
	go build -o $(BIN) ./cmd/brd

install:
	go build -o $(PREFIX)/bin/brd ./cmd/brd

test:
	go test ./...

race:
	go test ./... -race -count=1

vet:
	go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -rf bin
