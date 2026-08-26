BINARY := bin/mockapi
ADDR   ?= :8080
PKG    ?= ./...

# Release build. The workflow runs this same target, so what a tag publishes
# and what `make dist` produces cannot drift apart.
#
#   VERSION  stamped into `mockapi -version`; "dev" unless a tag says otherwise
#   TARGETS  os/arch pairs to build; override for just the one you need
DIST    := dist
VERSION ?= dev
TARGETS ?= linux/amd64 linux/arm64 darwin/arm64

.PHONY: all build dist run test cover fmt vet lint tidy clean

all: lint test build

build:
	go build -o $(BINARY) ./cmd/mockapi

# CGO_ENABLED=0 is what makes every target a plain cross-compile and the
# binary static — possible only because the SQLite driver is pure Go.
# -trimpath keeps local paths out of the binary (and makes the build
# reproducible); -s -w drops the symbol table and DWARF, a third of the size,
# and panics still name their functions.
dist:
	@rm -rf $(DIST)
	@mkdir -p $(DIST)
	@for target in $(TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "building $$target"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build \
			-trimpath \
			-ldflags "-s -w -X main.version=$(VERSION)" \
			-o "$(DIST)/mockapi_$(VERSION)_$${os}_$${arch}" ./cmd/mockapi || exit 1; \
	done
	@# The file list is taken before the redirect creates SHA256SUMS, which
	@# would otherwise end up checksumming itself.
	@cd $(DIST) && binaries=$$(ls) && \
		if command -v sha256sum >/dev/null 2>&1; \
		then sha256sum $$binaries; else shasum -a 256 $$binaries; fi > SHA256SUMS
	@ls -l $(DIST)

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
	rm -rf bin $(DIST) coverage.out
