BINARY := bin/mockapi
ADDR   ?= :8080
PKG    ?= ./...

.PHONY: all build run test cover fmt vet lint tidy clean

all: lint test build

build:
	go build -o $(BINARY) ./cmd/mockapi

run:
	go run ./cmd/mockapi -addr $(ADDR)

test:
	go test $(PKG)

cover:
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

fmt:
	gofmt -w .

vet:
	go vet $(PKG)

# gofmt -l prints files needing formatting; fail the build if any do.
lint: vet
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy:
	go mod tidy

clean:
	rm -rf bin coverage.out
